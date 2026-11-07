package agent

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// nodeRepo is the GitHub repo used for node self-updates.
const nodeRepo = "l8PYGIT/NodeAgent"

var versionRe = regexp.MustCompile(`^v?[0-9][0-9A-Za-z.\-]{0,30}$`)

// --- core lifecycle (master scope) ---

// coreStop stops the shared Xray core (all customers drop until started again).
func (s *Server) coreStop(w http.ResponseWriter, _ *http.Request) {
	s.mgr.Stop()
	writeJSON(w, http.StatusOK, map[string]any{"core_started": s.mgr.Started()})
}

// coreStart starts the core from the current config (no-op if already running
// with the same config; re-applies users).
func (s *Server) coreStart(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.RestartCore(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"core_started": s.mgr.Started(), "core_version": s.mgr.Version()})
}

// coreRestart restarts the core with the current config.
func (s *Server) coreRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.RestartCore(r.Context()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"core_started": s.mgr.Started(), "core_version": s.mgr.Version()})
}

// --- Xray-core version change (master scope) ---

type xrayVersionRequest struct {
	Version string `json:"version"` // e.g. v1.8.23; "latest" for the newest
	// SHA256 pins the expected archive digest. When empty the digest published
	// with the release is used instead.
	SHA256 string `json:"sha256,omitempty"`
	// AllowUnverified installs even when no checksum can be obtained. It exists
	// so an operator can consciously accept the risk; it is never the default,
	// because this binary runs as root on the node.
	AllowUnverified bool `json:"allow_unverified,omitempty"`
}

// setXrayVersion downloads the requested Xray-core release, replaces the node's
// xray binary and restarts the core.
func (s *Server) setXrayVersion(w http.ResponseWriter, r *http.Request) {
	var req xrayVersionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	version := req.Version
	if version == "" || version == "latest" {
		v, err := latestRelease("XTLS/Xray-core")
		if err != nil {
			writeError(w, http.StatusBadGateway, "resolve latest xray: "+err.Error())
			return
		}
		version = v
	}
	if !versionRe.MatchString(version) {
		writeError(w, http.StatusBadRequest, "invalid version")
		return
	}
	exePath := s.mgr.XrayExecutablePath()
	if err := downloadXray(version, exePath, req.SHA256, req.AllowUnverified); err != nil {
		writeError(w, http.StatusBadGateway, "download xray: "+err.Error())
		return
	}
	if err := s.mgr.RestartCore(r.Context()); err != nil {
		// The new binary is in place but the core will not run with it. Put the
		// previous binary back and try again: leaving a dead core behind a 200
		// meant the panel recorded a successful update while the node served
		// nothing.
		restored := false
		if rbErr := restoreBackup(exePath); rbErr != nil {
			log.Printf("agent: xray %s failed to start (%v) and rollback failed: %v", version, err, rbErr)
		} else if rsErr := s.mgr.RestartCore(r.Context()); rsErr != nil {
			log.Printf("agent: xray %s failed to start (%v); rolled back the binary but the core still will not start: %v", version, err, rsErr)
		} else {
			restored = true
			log.Printf("agent: xray %s failed to start (%v); rolled back to the previous binary", version, err)
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":        "xray did not start with the requested version: " + err.Error(),
			"xray_version": version,
			"rolled_back":  restored,
			"core_started": s.mgr.Started(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"xray_version": version, "core_started": s.mgr.Started(), "core_version": s.mgr.Version()})
}

// --- node binary self-update (master scope) ---

type nodeUpdateRequest struct {
	Version         string `json:"version"` // optional; default latest
	SHA256          string `json:"sha256,omitempty"`
	AllowUnverified bool   `json:"allow_unverified,omitempty"`
}

// selfUpdate downloads the requested NodeAgent release, replaces this binary and
// restarts the systemd service so the new version takes over.
func (s *Server) selfUpdate(w http.ResponseWriter, r *http.Request) {
	var req nodeUpdateRequest
	_ = decodeJSON(r, &req)
	version := req.Version
	if version == "" || version == "latest" {
		v, err := latestRelease(nodeRepo)
		if err != nil {
			writeError(w, http.StatusBadGateway, "resolve latest release: "+err.Error())
			return
		}
		version = v
	}
	if !versionRe.MatchString(version) {
		writeError(w, http.StatusBadRequest, "invalid version")
		return
	}
	exe, err := os.Executable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "locate executable: "+err.Error())
		return
	}
	if err := downloadNodeBinary(version, exe, req.SHA256, req.AllowUnverified); err != nil {
		writeError(w, http.StatusBadGateway, "download node binary: "+err.Error())
		return
	}
	// Restart the service in the background so the response is sent first. Use
	// systemd-run so the restart runs in its own transient scope and isn't killed
	// when this unit stops; fall back to setsid.
	unit := os.Getenv("PG_AGENT_SERVICE_NAME")
	if unit == "" {
		unit = "pg-node-agent"
	}
	go func() {
		time.Sleep(700 * time.Millisecond)
		if _, err := exec.LookPath("systemd-run"); err == nil {
			_ = exec.Command("systemd-run", "--no-block", "systemctl", "restart", unit).Start()
			return
		}
		_ = exec.Command("setsid", "systemctl", "restart", unit).Start()
	}()
	writeJSON(w, http.StatusOK, map[string]any{"updated_to": version, "restarting": true})
}

// --- helpers ---

func archTokens() (xrayArch, releaseArch string) {
	switch runtime.GOARCH {
	case "amd64":
		return "64", "amd64"
	case "arm64":
		return "arm64-v8a", "arm64"
	case "arm":
		return "arm32-v7a", "armv7"
	default:
		return "64", "amd64"
	}
}

func latestRelease(repo string) (string, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	m := regexp.MustCompile(`"tag_name":\s*"([^"]+)"`).FindSubmatch(body)
	if m == nil {
		return "", fmt.Errorf("no release found for %s", repo)
	}
	return string(m[1]), nil
}

// maxAssetBytes caps a downloaded release asset. io.ReadAll with no limit would
// happily buffer a multi-gigabyte (or endless) response into memory and OOM a
// small VPS; real Xray/agent archives are tens of megabytes.
const maxAssetBytes = 256 << 20

// maxChecksumBytes caps a checksum file, which is always tiny.
const maxChecksumBytes = 1 << 20

func httpDownload(url string) ([]byte, error) {
	return httpDownloadLimit(url, maxAssetBytes)
}

func httpDownloadLimit(url string, limit int64) ([]byte, error) {
	c := &http.Client{Timeout: 120 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	// Read one byte past the limit so an oversized body is detected rather than
	// silently truncated into a corrupt binary.
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", url, limit)
	}
	return data, nil
}

// verifyDigest checks data against an expected lowercase hex SHA-256.
func verifyDigest(data []byte, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return fmt.Errorf("no expected checksum supplied")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}

// sha256Re matches a bare hex SHA-256 digest anywhere in a checksum file.
var sha256Re = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

// xrayAssetDigest fetches the SHA-256 published alongside an Xray-core release
// asset (the ".dgst" sidecar) so the downloaded archive can be verified before
// it replaces a live binary.
func xrayAssetDigest(version, asset string) (string, error) {
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s.dgst", version, asset)
	body, err := httpDownloadLimit(url, maxChecksumBytes)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(strings.ToUpper(line), "SHA2-256") &&
			!strings.Contains(strings.ToUpper(line), "SHA256") {
			continue
		}
		if m := sha256Re.FindString(line); m != "" {
			return strings.ToLower(m), nil
		}
	}
	return "", fmt.Errorf("no SHA-256 entry in %s", url)
}

// releaseAssetDigest looks up an asset's SHA-256 in a release's checksums.txt.
func releaseAssetDigest(repo, version, asset string) (string, error) {
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", repo, version)
	body, err := httpDownloadLimit(url, maxChecksumBytes)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.Contains(line, asset) {
			continue
		}
		if m := sha256Re.FindString(line); m != "" {
			return strings.ToLower(m), nil
		}
	}
	return "", fmt.Errorf("no checksum for %s in %s", asset, url)
}

// verifyAsset checks a downloaded archive against a trusted digest.
//
// The replacement binary runs as root with CAP_NET_ADMIN, so a substituted or
// truncated download is a full node compromise, not a failed update. expected
// is the operator-pinned digest when supplied; otherwise the digest published
// alongside the release is used. allowUnverified is the explicit, logged escape
// hatch for a release that publishes no checksum at all.
func verifyAsset(data []byte, expected string, lookup func() (string, error), allowUnverified bool, what string) error {
	if expected != "" {
		return verifyDigest(data, expected)
	}
	want, err := lookup()
	if err == nil {
		return verifyDigest(data, want)
	}
	if allowUnverified {
		log.Printf("agent: WARNING: installing %s WITHOUT checksum verification (no published checksum: %v) because allow_unverified was set", what, err)
		return nil
	}
	return fmt.Errorf("cannot verify %s: %w (pass sha256, or allow_unverified to override)", what, err)
}

// downloadXray fetches Xray-linux-<arch>.zip for version, verifies it, and
// writes the "xray" binary to dst (atomically replacing it, keeping a backup).
func downloadXray(version, dst, expectedSHA256 string, allowUnverified bool) error {
	xrayArch, _ := archTokens()
	asset := fmt.Sprintf("Xray-linux-%s.zip", xrayArch)
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s", version, asset)
	data, err := httpDownload(url)
	if err != nil {
		return err
	}
	if err := verifyAsset(data, expectedSHA256,
		func() (string, error) { return xrayAssetDigest(version, asset) },
		allowUnverified, "xray "+version); err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != "xray" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeBinaryAtomic(dst, rc)
	}
	return fmt.Errorf("xray binary not found in archive")
}

// downloadNodeBinary fetches the release tarball for version, verifies it, and
// replaces dst with the contained pg-node-agent binary.
func downloadNodeBinary(version, dst, expectedSHA256 string, allowUnverified bool) error {
	_, relArch := archTokens()
	asset := fmt.Sprintf("pg-node-agent_linux_%s.tar.gz", relArch)
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", nodeRepo, version, asset)
	data, err := httpDownload(url)
	if err != nil {
		return err
	}
	if err := verifyAsset(data, expectedSHA256,
		func() (string, error) { return releaseAssetDigest(nodeRepo, version, asset) },
		allowUnverified, "pg-node-agent "+version); err != nil {
		return err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) == "pg-node-agent" {
			return writeBinaryAtomic(dst, tr)
		}
	}
	return fmt.Errorf("pg-node-agent binary not found in archive")
}

// backupSuffix names the copy of the previous binary kept for rollback.
const backupSuffix = ".bak"

// writeBinaryAtomic writes r to a temp file next to dst then renames over it,
// keeping the previous binary as dst.bak so a bad release can be rolled back
// rather than leaving the node with a binary that will not start.
func writeBinaryAtomic(dst string, r io.Reader) error {
	tmp := dst + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		if err := copyFile(dst, dst+backupSuffix); err != nil {
			log.Printf("agent: could not back up %s before replacing it: %v", dst, err)
		}
	}
	return os.Rename(tmp, dst)
}

// restoreBackup puts dst.bak back in place after a failed upgrade.
func restoreBackup(dst string) error {
	bak := dst + backupSuffix
	if _, err := os.Stat(bak); err != nil {
		return fmt.Errorf("no backup at %s: %w", bak, err)
	}
	return copyFile(bak, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst + ".tmp")
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst + ".tmp")
		return err
	}
	return os.Rename(dst+".tmp", dst)
}
