# Tool installer

Make targets use `install.py` to install pinned upstream tool binaries and
verify their SHA256 checksums. Tool modules, release assets, and checksum
locations are defined in `data.json`; Make supplies the pinned version.

If a release download fails with HTTP 408, 429, or 5xx, or a network connection
or response fails, the installer immediately falls back to building the same
pinned version from its Go module through `source.sh`. Missing releases and
unsupported platforms also use this source path. Source builds retain their
existing retry and cache behavior; checksum failures and other HTTP errors
still fail the installation.

Source installation requires the Go module and its dependencies to be available
through the configured Go module proxy or cache. A failed source installation
does not mark the requested release as installed.

Run `make verify-ci-tools` from the repository root to test installer behavior
without network access or containers. The same tests run in `make verify`.
