/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scraper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func GetFreePort() (int, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	l.Close()
	if taddr, isTCP := l.Addr().(*net.TCPAddr); isTCP {
		return taddr.Port, nil
	}
	return 0, errors.New("cannot get a free tcp address")
}

type Scraper struct {
	interval       time.Duration
	url            string
	fileNameFormat string
	c              http.Client
}

func NewScraper(interval time.Duration, url, fileNameFormat string) *Scraper {
	return &Scraper{
		interval:       interval,
		url:            url,
		fileNameFormat: fileNameFormat,
		c:              http.Client{},
	}
}

func (s *Scraper) doScrape(ctx context.Context, tw *tar.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return err
	}

	start := time.Now()

	resp, err := s.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	tmp := bytes.NewBuffer(nil)
	contentLen, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return err
	}

	hdr := &tar.Header{
		Name:    fmt.Sprintf(s.fileNameFormat, start.UnixMilli()),
		Size:    contentLen,
		Mode:    0666,
		ModTime: start,
	}

	err = tw.WriteHeader(hdr)
	if err != nil {
		return err
	}

	_, err = io.Copy(tw, tmp)
	return err
}

func (s *Scraper) Run(ctx context.Context, output string) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	out, err := os.Create(output)
	if err != nil {
		return err
	}

	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := s.doScrape(ctx, tw)
			if err != nil {
				return err
			}
		}
	}
}
