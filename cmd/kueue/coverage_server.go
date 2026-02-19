package main

// Inspired by: https://github.com/psturc/go-coverage-http
//
// NOTE: This file is injected into arbitrary Go "package main" directories by
// doozer's coverage instrumentation.  All imports are aliased with a "_cov"
// prefix and all top-level identifiers use a "_cov" prefix to avoid name
// collisions with identifiers declared by the host package (e.g. many
// projects declare ``var log = …`` at the package level).
//
// Multiple containers in a pod may each start a coverage server.  The server
// tries ports starting at _covDefaultPort (or COVERAGE_PORT) and increments
// up to _covMaxRetries times until it finds a free port.
//
// Clients can identify a coverage server by sending a HEAD request to any
// endpoint: the response will include the headers:
//   X-Art-Coverage-Server:         1
//   X-Art-Coverage-Pid:            <pid>
//   X-Art-Coverage-Binary:         <binary-name>
//   X-Art-Coverage-Source-Commit:  <commit>  (if SOURCE_GIT_COMMIT is set)
//   X-Art-Coverage-Source-Url:     <url>     (if SOURCE_GIT_URL is set)
//   X-Art-Coverage-Software-Group: <group>   (if SOFTWARE_GROUP or __doozer_group is set)
//   X-Art-Coverage-Software-Key:   <key>     (if SOFTWARE_KEY or __doozer_key is set)
//
// The producer records ALL X-Art-Coverage-* headers into info.json, converting
// header names to lowercase with dashes replaced by underscores.

import (
	_covBytes "bytes"
	_covBase64 "encoding/base64"
	_covJSON "encoding/json"
	_covFmt "fmt"
	_covLog "log"
	_covNet "net"
	_covHTTP "net/http"
	_covOS "os"
	_covPath "path/filepath"
	_covRuntime "runtime/coverage"
	_covStrconv "strconv"
	_covTime "time"
)

const (
	_covDefaultPort = 53700 // Starting port for the coverage server
	_covMaxRetries  = 50    // Maximum number of ports to try
)

// _covCachedHash stores the metadata hash after the first collection so
// that subsequent requests with ?nometa=1 can still produce correct
// counter filenames.
var _covCachedHash string

// _covResponse represents the JSON response from the coverage endpoint
type _covResponse struct {
	MetaFilename     string `json:"meta_filename"`
	MetaData         string `json:"meta_data"` // base64 encoded
	CountersFilename string `json:"counters_filename"`
	CountersData     string `json:"counters_data"` // base64 encoded
	Timestamp        int64  `json:"timestamp"`
}

func init() {
	// Start coverage server in a separate goroutine
	go _covStartServer()
}

// _covIdentityMiddleware wraps a handler to add identification headers to
// every response (including HEAD requests) so that clients can confirm they
// are talking to a coverage server rather than an unrelated process.
func _covIdentityMiddleware(next _covHTTP.Handler) _covHTTP.Handler {
	pid := _covStrconv.Itoa(_covOS.Getpid())
	exe := "unknown"
	if exePath, err := _covOS.Executable(); err == nil {
		exe = _covPath.Base(exePath)
	}

	// Optional environment variables — included as headers when set.
	// For SOFTWARE_GROUP / SOFTWARE_KEY, fall back to __doozer_group / __doozer_key.
	softwareGroup := _covOS.Getenv("SOFTWARE_GROUP")
	if softwareGroup == "" {
		softwareGroup = _covOS.Getenv("__doozer_group")
	}
	softwareKey := _covOS.Getenv("SOFTWARE_KEY")
	if softwareKey == "" {
		softwareKey = _covOS.Getenv("__doozer_key")
	}
	envHeaders := map[string]string{
		"X-Art-Coverage-Source-Commit":  _covOS.Getenv("SOURCE_GIT_COMMIT"),
		"X-Art-Coverage-Source-Url":     _covOS.Getenv("SOURCE_GIT_URL"),
		"X-Art-Coverage-Software-Group": softwareGroup,
		"X-Art-Coverage-Software-Key":   softwareKey,
	}

	return _covHTTP.HandlerFunc(func(w _covHTTP.ResponseWriter, r *_covHTTP.Request) {
		w.Header().Set("X-Art-Coverage-Server", "1")
		w.Header().Set("X-Art-Coverage-Pid", pid)
		w.Header().Set("X-Art-Coverage-Binary", exe)
		for header, val := range envHeaders {
			if val != "" {
				w.Header().Set(header, val)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// _covStartServer starts a dedicated HTTP server for coverage collection.
// It tries successive ports starting from COVERAGE_PORT (default 53700)
// until one is available or _covMaxRetries attempts are exhausted.
func _covStartServer() {
	startPort := _covDefaultPort
	if envPort := _covOS.Getenv("COVERAGE_PORT"); envPort != "" {
		if p, err := _covStrconv.Atoi(envPort); err == nil && p > 0 {
			startPort = p
		}
	}

	// Create a new ServeMux for the coverage server (isolated from main app)
	mux := _covHTTP.NewServeMux()
	mux.HandleFunc("/coverage", _covHandler)
	mux.HandleFunc("/health", func(w _covHTTP.ResponseWriter, r *_covHTTP.Request) {
		w.WriteHeader(_covHTTP.StatusOK)
		_covFmt.Fprintf(w, "coverage server healthy")
	})

	// Wrap with identity middleware so HEAD requests get recognition headers
	handler := _covIdentityMiddleware(mux)

	for attempt := 0; attempt < _covMaxRetries; attempt++ {
		port := startPort + attempt
		addr := _covFmt.Sprintf(":%d", port)

		// Try to bind the port before committing to ListenAndServe so we
		// can detect "address already in use" and try the next port.
		ln, err := _covNet.Listen("tcp", addr)
		if err != nil {
			_covLog.Printf("[COVERAGE] Port %d unavailable: %v; trying next", port, err)
			continue
		}

		_covLog.Printf("[COVERAGE] Starting coverage server on port %d (pid %d)", port, _covOS.Getpid())
		_covLog.Printf("[COVERAGE] Endpoints: GET %s/coverage, GET %s/health, HEAD %s/*", addr, addr, addr)

		// Serve on the already-bound listener (this blocks)
		if err := _covHTTP.Serve(ln, handler); err != nil {
			_covLog.Printf("[COVERAGE] ERROR: Coverage server on port %d failed: %v", port, err)
		}
		return
	}

	_covLog.Printf("[COVERAGE] ERROR: Could not bind any port in range %d–%d", startPort, startPort+_covMaxRetries-1)
}

// _covHandler collects coverage data and returns it via HTTP as JSON.
// Pass ?nometa=1 to skip metadata collection (useful after the first fetch
// since metadata does not change for the lifetime of the process).
func _covHandler(w _covHTTP.ResponseWriter, r *_covHTTP.Request) {
	skipMeta := r.URL.Query().Get("nometa") == "1"

	if skipMeta {
		_covLog.Println("[COVERAGE] Collecting coverage counters (metadata skipped)...")
	} else {
		_covLog.Println("[COVERAGE] Collecting coverage data...")
	}

	// Collect metadata unless the caller opted out
	var metaData []byte
	var metaFilename string
	if !skipMeta {
		var metaBuf _covBytes.Buffer
		if err := _covRuntime.WriteMeta(&metaBuf); err != nil {
			_covHTTP.Error(w, _covFmt.Sprintf("Failed to collect metadata: %v", err), _covHTTP.StatusInternalServerError)
			return
		}
		metaData = metaBuf.Bytes()
	}

	// Collect counters
	var counterBuf _covBytes.Buffer
	if err := _covRuntime.WriteCounters(&counterBuf); err != nil {
		_covHTTP.Error(w, _covFmt.Sprintf("Failed to collect counters: %v", err), _covHTTP.StatusInternalServerError)
		return
	}
	counterData := counterBuf.Bytes()

	// Extract hash from metadata to create proper filenames.
	// Cache the hash so that ?nometa=1 requests can still produce correct
	// counter filenames without re-collecting metadata.
	var hash string
	if len(metaData) >= 32 {
		hashBytes := metaData[16:32]
		hash = _covFmt.Sprintf("%x", hashBytes)
		_covCachedHash = hash
	} else if _covCachedHash != "" {
		hash = _covCachedHash
	} else {
		hash = "unknown"
	}

	if !skipMeta {
		metaFilename = _covFmt.Sprintf("covmeta.%s", hash)
	}

	// Generate counter filename
	timestamp := _covTime.Now().UnixNano()
	counterFilename := _covFmt.Sprintf("covcounters.%s.%d.%d", hash, _covOS.Getpid(), timestamp)

	_covLog.Printf("[COVERAGE] Collected %d bytes metadata, %d bytes counters",
		len(metaData), len(counterData))

	// Return coverage data as JSON
	response := _covResponse{
		MetaFilename:     metaFilename,
		CountersFilename: counterFilename,
		CountersData:     _covBase64.StdEncoding.EncodeToString(counterData),
		Timestamp:        timestamp,
	}
	if !skipMeta {
		response.MetaData = _covBase64.StdEncoding.EncodeToString(metaData)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := _covJSON.NewEncoder(w).Encode(response); err != nil {
		_covLog.Printf("[COVERAGE] Error encoding response: %v", err)
		_covHTTP.Error(w, "Failed to encode response", _covHTTP.StatusInternalServerError)
		return
	}

	_covLog.Println("[COVERAGE] Coverage data sent successfully")
}
