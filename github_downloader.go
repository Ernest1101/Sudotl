/*
 * SPDX-License-Identifier: GPL-3.0
 * Vencord Installer, a cross platform gui/cli app for installing Vencord
 * Copyright (c) 2023 Vendicated and Vencord contributors
 */

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	path "path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type GithubRelease struct {
	Name    string `json:"name"`
	TagName string `json:"tag_name"`
	Prerelease bool `json:"prerelease"`
	Assets  []struct {
		Name        string `json:"name"`
		DownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

var ReleaseData GithubRelease
var GithubError error
var GithubDoneChan chan bool

var InstalledHash = "None"
var LatestHash = "Unknown"
var IsDevInstall bool

// Release info requests get a timeout so a blocked GitHub fails fast and the mirror is tried
var apiClient = &http.Client{Timeout: 20 * time.Second}

// openDownload starts a download and returns the response only for a 2xx status
func openDownload(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		res.Body.Close()
		return nil, errors.New(res.Status)
	}
	return res, nil
}

// fetchReleases accepts both a release list (/releases) and a single release (/releases/latest)
func fetchReleases(url string) ([]GithubRelease, error) {
	Log.Debug("Fetching", url)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)

	res, err := apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %s", url, res.Status)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var releases []GithubRelease
	if err = json.Unmarshal(body, &releases); err == nil {
		return releases, nil
	}

	var single GithubRelease
	if err = json.Unmarshal(body, &single); err != nil {
		return nil, fmt.Errorf("failed to decode release JSON from %s: %w", url, err)
	}
	return []GithubRelease{single}, nil
}

func GetGithubRelease(url, fallbackUrl string) (*GithubRelease, error) {
	releases, err := fetchReleases(url)
	if err != nil && fallbackUrl != "" && fallbackUrl != url {
		// any failure counts: GitHub may be rate limited, blocked by the ISP or unreachable altogether
		Log.Warn(fmt.Sprintf("Failed to fetch %s (%v). Trying fallback url %s", url, err, fallbackUrl))
		var fallbackErr error
		releases, fallbackErr = fetchReleases(fallbackUrl)
		if fallbackErr != nil {
			err = fmt.Errorf("%v; fallback: %v", err, fallbackErr)
		} else {
			err = nil
		}
	}
	if err != nil {
		Log.Error("Failed to fetch release info:", err)
		return nil, err
	}

	if len(releases) == 0 {
		err = errors.New("no releases found")
		Log.Error(err.Error())
		return nil, err
	}

	// prefer the devbuild prerelease; fall back to the newest stable
	data := releases[0]
	for _, rel := range releases {
		if rel.TagName == "devbuild" || rel.Prerelease {
			data = rel
			break
		}
	}
	return &data, nil
}

func InitGithubDownloader() {
	GithubDoneChan = make(chan bool, 1)

	IsDevInstall = os.Getenv("SUDOCORD_DEV_INSTALL") == "1"
	Log.Debug("Is Dev Install: ", IsDevInstall)
	if IsDevInstall {
		GithubDoneChan <- true
		return
	}

	go func() {
		// Make sure UI updates once the request either finished or failed
		defer func() {
			GithubDoneChan <- GithubError == nil
		}()

		data, err := GetGithubRelease(ReleaseUrl, ReleaseUrlFallback)
		if err != nil {
			GithubError = err
			return
		}

		ReleaseData = *data

		i := strings.LastIndex(data.Name, " ") + 1
		LatestHash = data.Name[i:]
		Log.Debug("Finished fetching GitHub Data")
		Log.Debug("Latest hash is", LatestHash, "Local Install is", Ternary(LatestHash == InstalledHash, "up to date!", "outdated!"))
	}()

	// either .asar file or directory with main.js file (in DEV)
	SudoCordFile := SudoCordDirectory

	stat, err := os.Stat(SudoCordFile)
	if err != nil {
		return
	}

	// dev
	if stat.IsDir() {
		SudoCordFile = path.Join(SudoCordFile, "main.js")
	}

	// Check hash of installed version if exists
	b, err := os.ReadFile(SudoCordFile)
	if err != nil {
		return
	}

	Log.Debug("Found existing SudoCord Install. Checking for hash...")

	re := regexp.MustCompile(`// (?:SudoCord|Vencord) (\w+)`)
	match := re.FindSubmatch(b)
	if match != nil {
		InstalledHash = string(match[1])
		Log.Debug("Existing hash is", InstalledHash)

	} else {
		Log.Debug("Didn't find hash")

	}
}

func installLatestBuilds() (retErr error) {
	Log.Debug("Installing latest builds...")

	if IsDevInstall {
		Log.Debug("Skipping due to dev install")
		return
	}

	downloadUrl := ""
	for _, ass := range ReleaseData.Assets {
		if ass.Name == "desktop.asar" {
			downloadUrl = ass.DownloadURL
			break
		}
	}

	if downloadUrl == "" {
		retErr = errors.New("Didn't find desktop.asar download link")
		Log.Error(retErr)
		return
	}

	Log.Debug("Downloading desktop.asar")

	res, err := openDownload(downloadUrl)
	if err != nil && downloadUrl != MirrorAsarUrl {
		// GitHub release files are blocked on some networks, the site mirror has the same build
		Log.Warn("Failed to download desktop.asar from", downloadUrl+":", err, "- trying mirror", MirrorAsarUrl)
		res, err = openDownload(MirrorAsarUrl)
	}
	if err != nil {
		Log.Error("Failed to download desktop.asar:", err)
		retErr = err
		return
	}
	defer res.Body.Close()

	out, err := os.OpenFile(SudoCordDirectory, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		Log.Error("Failed to create", SudoCordDirectory+":", err)
		retErr = err
		return
	}
	read, err := io.Copy(out, res.Body)
	if err != nil {
		Log.Error("Failed to download to", SudoCordDirectory+":", err)
		retErr = err
		return
	}
	contentLength := res.Header.Get("Content-Length")
	expected := strconv.FormatInt(read, 10)
	if expected != contentLength {
		err = errors.New("Unexpected end of input. Content-Length was " + contentLength + ", but I only read " + expected)
		Log.Error(err.Error())
		retErr = err
		return
	}

	_ = FixOwnership(SudoCordDirectory)

	InstalledHash = LatestHash
	return
}
