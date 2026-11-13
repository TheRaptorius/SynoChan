// Command spkindex writes the JSON index that Package Center reads from a package source.
//
//	spkindex -download-base https://github.com/TheRaptorius/SynoChan/releases/download/v1.0.0 \
//	         -site https://theraptorius.github.io/SynoChan dist/synochan-1.0.0-0001-noarch.spk > site/index.json
package main

import (
	"archive/tar"
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Package is one entry of the source index, in the format of Synology's package server.
type Package struct {
	Package             string   `json:"package"`
	Version             string   `json:"version"`
	DName               string   `json:"dname"`
	Desc                string   `json:"desc"`
	Price               int      `json:"price"`
	DownloadCount       int      `json:"download_count"`
	RecentDownloadCount int      `json:"recent_download_count"`
	Link                string   `json:"link"`
	Size                int64    `json:"size"`
	MD5                 string   `json:"md5"`
	Thumbnail           []string `json:"thumbnail"`
	Snapshot            []string `json:"snapshot"`
	QInst               bool     `json:"qinst"`
	QStart              bool     `json:"qstart"`
	QUpgrade            bool     `json:"qupgrade"`
	DepSers             *string  `json:"depsers"`
	DepPkgs             *string  `json:"deppkgs"`
	ConflictPkgs        *string  `json:"conflictpkgs"`
	Start               bool     `json:"start"`
	Maintainer          string   `json:"maintainer"`
	MaintainerURL       string   `json:"maintainer_url"`
	Distributor         string   `json:"distributor"`
	DistributorURL      string   `json:"distributor_url"`
	SupportURL          string   `json:"support_url"`
	Changelog           string   `json:"changelog"`
	ThirdParty          bool     `json:"thirdparty"`
	Category            int      `json:"category"`
	Subcategory         int      `json:"subcategory"`
	Type                int      `json:"type"`
	SilentInstall       bool     `json:"silent_install"`
	SilentUninstall     bool     `json:"silent_uninstall"`
	SilentUpgrade       bool     `json:"silent_upgrade"`
	AutoUpgradeFrom     *string  `json:"auto_upgrade_from"`
	Beta                bool     `json:"beta"`
	OSMinVer            string   `json:"os_min_ver,omitempty"`
}

// Index is the whole response.
type Index struct {
	Packages []Package `json:"packages"`
	Keyrings []string  `json:"keyrings"`
}

func main() {
	downloadBase := flag.String("download-base", "", "URL the SPK files are downloaded from (file name is appended)")
	site := flag.String("site", "", "URL of the package source site, where icons are published")
	beta := flag.Bool("beta", false, "mark packages as beta")
	flag.Parse()
	if *downloadBase == "" || *site == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: spkindex -download-base URL -site URL file.spk...")
		os.Exit(2)
	}
	idx := Index{Packages: []Package{}, Keyrings: []string{}}
	for _, path := range flag.Args() {
		p, err := describe(path, strings.TrimRight(*downloadBase, "/"), strings.TrimRight(*site, "/"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		p.Beta = *beta
		idx.Packages = append(idx.Packages, p)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(idx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func describe(path, downloadBase, site string) (Package, error) {
	f, err := os.Open(path)
	if err != nil {
		return Package{}, err
	}
	defer f.Close()

	h := md5.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return Package{}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Package{}, err
	}
	info, err := readINFO(f)
	if err != nil {
		return Package{}, err
	}
	if info["package"] == "" || info["version"] == "" {
		return Package{}, errors.New("INFO lacks package or version")
	}
	return Package{
		Package:        info["package"],
		Version:        info["version"],
		DName:          info["displayname"],
		Desc:           info["description"],
		Link:           downloadBase + "/" + filepath.Base(path),
		Size:           size,
		MD5:            hex.EncodeToString(h.Sum(nil)),
		Thumbnail:      []string{site + "/icon_72.png", site + "/icon_256.png"},
		Snapshot:       []string{},
		QInst:          true,
		QStart:         true,
		QUpgrade:       true,
		Start:          true,
		Maintainer:     info["maintainer"],
		MaintainerURL:  info["maintainer_url"],
		Distributor:    info["distributor"],
		DistributorURL: info["distributor_url"],
		SupportURL:     info["support_url"],
		Changelog:      info["changelog"],
		ThirdParty:     true,
		SilentUpgrade:  info["silent_upgrade"] == "yes",
		OSMinVer:       info["os_min_ver"],
	}, nil
}

// readINFO returns the key="value" pairs of the INFO file in an SPK.
func readINFO(r io.Reader) (map[string]string, error) {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no INFO file in package")
		}
		if err != nil {
			return nil, err
		}
		if strings.TrimPrefix(hdr.Name, "./") != "INFO" {
			continue
		}
		out := map[string]string{}
		sc := bufio.NewScanner(tr)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), "=")
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			v = strings.TrimSuffix(strings.TrimPrefix(v, `"`), `"`)
			out[strings.TrimSpace(k)] = strings.ReplaceAll(v, `\"`, `"`)
		}
		return out, sc.Err()
	}
}
