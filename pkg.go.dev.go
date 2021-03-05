package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gagliardetto/request"
	. "github.com/gagliardetto/utilz"
)

// GetImportersOfGolangPackage gets a list of importers of a Golang package
// from pkg.go.dev.
func GetImportersOfGolangPackage(pkgPath string, limit int) ([]string, error) {
	req := request.NewRequest(httpClient)

	pkgPath = strings.TrimSpace(pkgPath)
	pkgPath = strings.TrimPrefix(pkgPath, "https://")
	pkgPath = strings.TrimPrefix(pkgPath, "http://")
	pkgPath = strings.TrimPrefix(pkgPath, "/")
	pkgPath = strings.TrimSuffix(pkgPath, "/")
	resp, err := req.Get("https://pkg.go.dev/" + pkgPath + "?tab=importedby")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	defer closer()

	deps, err := getImportersOfGolangPackage(reader)
	if err != nil {
		return nil, err
	}

	if limit > 0 && len(deps) > limit {
		deps = deps[:limit-1]
	}

	return deps, nil
}

func getImportersOfGolangPackage(reader io.Reader) ([]string, error) {
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return nil, fmt.Errorf("error while goquery.NewDocumentFromReader: %s", err)
	}

	// rawDependants will contain the raw URLs (of potentially the subpackages)
	var rawDependants []string

	// Find the items
	doc.Find(".u-breakWord").Each(func(i int, s *goquery.Selection) {
		href, ok := s.Attr("href")
		if ok {
			trimmed := strings.TrimPrefix(href, `/`)

			rawDependants = append(rawDependants, trimmed)
		}
	})

	rawDependants = Deduplicate(rawDependants)

	// rootDependants are the package paths of the importers:
	var rootDependants []string

	for _, dependant := range rawDependants {
		isSupported := strings.HasPrefix(dependant, "github.com/") || strings.HasPrefix(dependant, "gitlab.org/") || strings.HasPrefix(dependant, "bitbucket.org/")
		// NOTE: we are skipping anything that is not on github, gitlab, or bitbucket.
		if isSupported {
			parts := strings.Split(dependant, "/")
			if len(parts) < 3 {
				continue
			}
			root := "https://" + strings.Join(parts[:3], "/")

			rootDependants = append(rootDependants, root)
		}
	}

	rootDependants = Deduplicate(rootDependants)

	return rootDependants, nil
}
