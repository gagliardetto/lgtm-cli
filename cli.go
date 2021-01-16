package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gagliardetto/bianconiglio"
	"github.com/gagliardetto/eta"
	ghc "github.com/gagliardetto/gh-client"
	. "github.com/gagliardetto/utilz"
	"github.com/google/go-github/github"
	"github.com/goware/urlx"
	"github.com/hako/durafmt"
	"github.com/urfave/cli"
	"go.uber.org/ratelimit"
)

const (
	githubHost  = "https://github.com"
	defaultHost = githubHost
)

var (
	apiRateLimiter = ratelimit.New(1, ratelimit.WithoutSlack)
	ghClient       *ghc.Client
)

func main() {
	var configFilepath string
	var client *Client
	var waitDuration time.Duration
	var ignoreFollowedErrors bool

	///////////////////////////////////////////////////////////////////////////////////////////////////////////////
	unfollower := func(isProto bool, key string, name string, etac *eta.ETA) {
		defer etac.Done(1)

		averagedETA := etac.GetETA()
		thisETA := durafmt.Parse(averagedETA.Round(time.Second)).String()

		Infof(
			"[%s](%v/%v) Unfollowing %s ... ETA %s",
			etac.GetFormattedPercentDone(),
			etac.GetDone()+1,
			etac.GetTotal(),
			name,
			thisETA,
		)

		unfollowFunc := client.UnfollowProject
		if isProto {
			unfollowFunc = client.UnfollowProtoProject
		}

		err := unfollowFunc(key)
		if err != nil {
			Errorf(
				"error while unfollowing project %s: %s",
				name,
				err,
			)
		} else {
			Successf(
				"[%s](%v/%v) Unfollowed %s; ETA %s",
				etac.GetFormattedPercentDone(),
				etac.GetDone()+1,
				etac.GetTotal(),
				name,
				thisETA,
			)
		}
	}

	follower := func(u string, etac *eta.ETA) *Envelope {
		defer etac.Done(1)

		averagedETA := etac.GetETA()
		thisETA := durafmt.Parse(averagedETA.Round(time.Second)).String()

		Infof(
			"[%s](%v/%v) Following %s ...; ETA %s",
			etac.GetFormattedPercentDone(),
			etac.GetDone()+1,
			etac.GetTotal(),
			u,
			thisETA,
		)

		prj, err := client.FollowProject(u)
		if err != nil {
			Errorf(
				"error while following project %s : %s",
				u,
				err,
			)
		} else {
			var knownOrNew string
			if prj.IsKnown() {
				knownOrNew = OrangeBG("[KNO]")
			} else {
				knownOrNew = LimeBG("[NEW]")
			}
			Successf(
				"[%s](%v/%v) Followed %s %s; ETA %s",
				etac.GetFormattedPercentDone(),
				etac.GetDone()+1,
				etac.GetTotal(),
				knownOrNew,
				u,
				thisETA,
			)
		}
		return prj
	}

	///////////////////////////////////////////////////////////////////////////////////////////////////////////////
	app := &cli.App{
		Name:        "lgtm-cli",
		Description: "Unofficial lgtm.com CLI — https://github.com/gagliardetto/lgtm-cli",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "conf",
				Usage:       "Path to credentials.json file",
				Destination: &configFilepath,
			},
			&cli.DurationFlag{
				Name:        "wait",
				Usage:       "Wait duration between requests.",
				Destination: &waitDuration,
			},
			&cli.BoolFlag{
				Name:        "ignore-followed-errors",
				Usage:       "Ignore errors that happen while getting list of followed projects (when that is acceptable).",
				Destination: &ignoreFollowedErrors,
			},
		},
		Before: func(c *cli.Context) error {

			configFilepathFromEnv := os.Getenv("LGTM_CLI_CONFIG")

			if configFilepath == "" && configFilepathFromEnv == "" {
				Errorf("No config provided. Please specify the path to the config file with the LGTM_CLI_CONFIG env var.")
				return errors.New(c.App.Usage)
			}

			// If the conf flag is not set, use env variable:
			if configFilepath == "" {
				configFilepath = configFilepathFromEnv
			}

			conf, err := LoadConfigFromFile(configFilepath)
			if err != nil {
				Fatalf("Wrror while loading config: %s", err)
			}
			if err := conf.Validate(); err != nil {
				Fatalf("Config is not valid: %s", err)
			}

			client, err = NewClient(conf)
			if err != nil {
				panic(err)
			}

			// Setup a new github client:
			ghClient = ghc.NewClient(conf.GitHub.Token)

			ghc.ResponseCallback = func(resp *github.Response) {
				if resp == nil {
					return
				}
				if resp.Rate.Remaining < 1000 {
					Warnf(
						"GitHub API rate: remaining %v/%v; resetting in %s",
						resp.Rate.Remaining,
						resp.Rate.Limit,
						resp.Rate.Reset.Sub(time.Now()).Round(time.Second),
					)
				}
			}
			return nil
		},
		Commands: []cli.Command{
			{
				Name:  "unfollow-all",
				Usage: "Unfollow all currently followed repositories (a.k.a. \"projects\").",
				Action: func(c *cli.Context) error {
					cache, err := client.GetFollowedCache()
					if err != nil {
						panic(err)
					}

					totalProjects := cache.NumProjects()
					totalProtoProjects := cache.NumProto()
					total := totalProjects + totalProtoProjects

					Infof("You are following a total of %v repos", total)

					if total == 0 {
						return nil
					}
					Infof("Starting to unfollow all...")

					etac := eta.New(int64(total))

					for _, proto := range cache.ProtoProjects() {
						unfollower(true, proto.Key, proto.CloneURL, etac)
					}
					for _, pr := range cache.Projects() {
						unfollower(false, pr.Key, pr.ExternalURL.URL, etac)
					}

					return nil
				},
			},
			{
				Name:  "unfollow",
				Usage: "Unfollow one or more projects.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos (can use flag multiple times).",
					},
				},
				Action: func(c *cli.Context) error {
					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						// Load repo list from file(s):
						repoListFilepaths := c.StringSlice("f")
						repoURLsRaw = append(repoURLsRaw, mustLoadTargetsFromFilepaths(repoListFilepaths...)...)
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLPatterns := make([]string, 0)

					// Compile list of patterns:
					for _, raw := range repoURLsRaw {
						parsed, err := ParseGitURL(raw, false)
						if err != nil {
							panic(err)
						}
						if isGlob(raw) {
							repoURLPatterns = append(repoURLPatterns, parsed.URL())
						} else {
							_, isWholeUser, err := IsUserOnly(raw)
							if err != nil {
								panic(err)
							}
							if isWholeUser {
								// Transform to a glob that matches all repos of a user:
								asGlob := parsed.URL() + "/*"
								repoURLPatterns = append(repoURLPatterns, asGlob)
							} else {
								repoURLPatterns = append(repoURLPatterns, parsed.URL())
							}
						}
					}

					matchAllPatterns := getGlobsThatMatchEverything(repoURLPatterns)
					if len(matchAllPatterns) > 0 {
						Infof("The following patterns will match all followed projects, and consequently *all* followed projects will be unfollowed.")
						Infof("%s", Sq(matchAllPatterns))
						CLIMustConfirmYes("Do you really want to unfollow all projects?")
					}

					cache, err := client.GetFollowedCache()
					hasCache := err == nil && cache != nil
					if !hasCache {
						if ignoreFollowedErrors {
							Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
						} else {
							panic(err)
						}
					}
					if hasCache {
						// We got the list of followed projects, so we can use it:

						// Match projects against list of repos followed:
						projectsToBeUnfollowed := Filter(cache.Projects(),
							func(i int, pr *Project) bool {
								_, isToBeUnfollowed := HasMatch(pr.ExternalURL.URL, repoURLPatterns)
								return isToBeUnfollowed
							}).([]*Project)

						protoToBeUnfollowed := Filter(cache.ProtoProjects(),
							func(i int, pr *ProtoProject) bool {
								_, isToBeUnfollowed := HasMatch(trimDotGit(pr.CloneURL), repoURLPatterns)
								return isToBeUnfollowed
							}).([]*ProtoProject)

						Infof(
							"Will unfollow %v projects and %v proto-projects...",
							len(projectsToBeUnfollowed),
							len(protoToBeUnfollowed),
						)
						total := len(projectsToBeUnfollowed) + len(protoToBeUnfollowed)
						if total == 0 {
							return nil
						}

						etac := eta.New(int64(total))

						// Unfollow projects:
						for _, pr := range projectsToBeUnfollowed {
							message := pr.ExternalURL.URL

							pattern, matched := HasMatch(pr.ExternalURL.URL, repoURLPatterns)
							if matched {
								message += " " + Sf("(matched from %s pattern)", Lime(pattern))
							}
							unfollower(false, pr.Key, message, etac)
						}
						// Unfollow proto-projects:
						for _, pr := range protoToBeUnfollowed {
							message := pr.CloneURL

							pattern, matched := HasMatch(trimDotGit(pr.CloneURL), repoURLPatterns)
							if matched {
								message += " " + Sf("(matched from %s pattern)", Lime(pattern))
							}
							unfollower(true, pr.Key, message, etac)
						}
					} else {
						// we don't have the cache, so let's unfollow anything we can
						// with the information we have:
						projectKeys := make(map[string]string)
						for _, repoURL := range repoURLPatterns {
							if isGlob(repoURL) {
								// Skip because not a complete URL.
								Infof("Skipping %s", repoURL)
								continue
							}
							parsed, err := ParseGitURL(repoURL, true)
							if err != nil {
								panic(err)
							}
							isWholeUser := parsed.Repo == ""
							if isWholeUser {
								// Skip because not a complete URL.
								Infof("Skipping %s", repoURL)
								continue
							}

							pr, err := client.GetProjectBySlug(parsed.Slug())
							if err != nil {
								if isStatusResponseError(err) && err.(*StatusResponse).IsNotFound() {
									Warnf(
										"Project %s is not a built project.",
										trimGithubPrefix(repoURL),
									)
								} else {
									// General error
									panic(err)
								}
							} else {
								projectKeys[pr.ExternalURL.URL] = pr.Key
							}
						}

						etac := eta.New(int64(len(projectKeys)))
						for projectURL, projectKey := range projectKeys {
							unfollower(false, projectKey, projectURL, etac)
						}
					}
					return nil
				},
			},
			{
				Name:  "follow",
				Usage: "Follow one or more projects.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
					&cli.StringFlag{
						Name:  "lang, l",
						Usage: "Filter github repos by language.",
					},
					&cli.StringFlag{
						Name:  "output, o",
						Usage: "File name to write repositories to.",
					},
				},
				Action: func(c *cli.Context) error {

					lang := ToLower(c.String("lang"))

					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						repoListFilepaths := c.StringSlice("f")
						repoURLsRaw = append(repoURLsRaw, mustLoadTargetsFromFilepaths(repoListFilepaths...)...)
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLs := make([]string, 0)
					for _, raw := range repoURLsRaw {
						owner, isWholeUser, err := IsUserOnly(raw)
						if err != nil {
							panic(err)
						}
						if isWholeUser {
							Debugf("Getting list of repos for %s ...", owner)

							var repos []*github.Repository
							if lang != "" {
								repos, err = GithubListReposByLanguage(owner, lang)
								if err != nil {
									panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
								}
							} else {
								repos, err = GithubGetRepoList(owner)
								if err != nil {
									panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
								}
							}
							Debugf("%s has %v repos", owner, len(repos))
						RepoLoop:
							for _, repo := range repos {
								//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
								isFork := repo.GetFork()
								// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
								if isFork {
									Warnf("Skipping fork %s", repo.GetFullName())
									continue RepoLoop
								}

								repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
							}
						} else {
							parsed, err := ParseGitURL(raw, false)
							if err != nil {
								panic(err)
							}
							repoURLs = append(repoURLs, parsed.URL())
						}
					}

					toBeFollowed := repoURLs
					cache, err := client.GetFollowedCache()
					hasCache := err == nil && cache != nil
					if !hasCache {
						if ignoreFollowedErrors {
							Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
						} else {
							panic(err)
						}
					} else {
						// Exclude already-followed projects:
						toBeFollowed = cache.RemoveFollowed(repoURLs)
					}
					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile(c.String("output"), "follow", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// Follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// If the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
			},
			{
				Name:  "follow-by-lang",
				Usage: "Follow projects by language.",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max number of projects to get and follow.",
					},
					&cli.IntFlag{
						Name:  "start",
						Usage: "Start following from project N of the final list (one-indexed).",
					},
					&cli.BoolFlag{
						Name:  "force, y",
						Usage: "Don't ask for confirmation.",
					},
					&cli.StringFlag{
						Name:  "output, o",
						Usage: "File name to write repositories to.",
					},
				},
				Action: func(c *cli.Context) error {

					lang := c.Args().First()
					if lang == "" {
						Fatalf("Must provide a language")
					}
					limit := c.Int("limit")
					start := c.Int("start")
					force := c.Bool("y")

					repoURLs := make([]string, 0)
					{
						Debugf("Getting list of repos for language: %s ...", lang)

						repos, err := GithubListAllReposByLanguage(lang, limit)
						if err != nil {
							Fatalf("error while getting repo list for language %q: %s", lang, err)
						}

						Debugf("%s has %v repos", lang, len(repos))
					RepoLoop:
						for _, repo := range repos {
							//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
							isFork := repo.GetFork()
							// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
							if isFork {
								Warnf("Skipping fork %s", repo.GetFullName())
								continue RepoLoop
							}

							repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
						}
					}
					{ // Trim repoURLs if --start is provided.
						if start > 0 && start > len(repoURLs) {
							Fatalf(
								"Got %v projects, but the --start flag value is set to %v",
								len(repoURLs),
								start,
							)
						}
						if start > 0 {
							Infof("Skipping %v projects", start-1)
							repoURLs = repoURLs[start-1:]
						}
					}

					toBeFollowed := repoURLs
					cache, err := client.GetFollowedCache()
					hasCache := err == nil && cache != nil
					if !hasCache {
						if ignoreFollowedErrors {
							Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
						} else {
							panic(err)
						}
					} else {
						// Exclude already-followed projects:
						toBeFollowed = cache.RemoveFollowed(repoURLs)
					}
					totalToBeFollowed := len(toBeFollowed)

					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile(c.String("output"), "follow-by-lang", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// Follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// If the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
			},
			{
				Name:  "follow-by-meta-search",
				Usage: "Follow projects by custom search on repositories meta.",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max number of projects to get and follow.",
					},
					&cli.BoolFlag{
						Name:  "force, y",
						Usage: "Don't ask for confirmation.",
					},
					&cli.StringFlag{
						Name:  "output, o",
						Usage: "File name to write repositories to.",
					},
				},
				Action: func(c *cli.Context) error {

					query := c.Args().First()
					if query == "" {
						Fataln("must provide a query string")
					}
					if !strings.Contains(query, "fork:false") {
						Warnf("The provided query does not exclude forks (lgtm.com does not support scanning forks).")
						Warnf("The results will contain forks, and that will reduce the number of usable results (the API can only return 1K results max).")
						Warnf("You can exclude forks by adding fork:false to your query.")
					}
					limit := c.Int("limit")
					force := c.Bool("y")

					repoURLs := make([]string, 0)
					{
						Debugf("Getting list of repos for search: %s ...", ShakespeareBG(query))
						repos, err := GithubListReposByMetaSearch(query, limit)
						if err != nil {
							Fatalf("error while getting repo list for search %q: %s", query, err)
						}

						Debugf("Search %s has returned %v repos", ShakespeareBG(query), len(repos))
					RepoLoop:
						for _, repo := range repos {
							//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
							isFork := repo.GetFork()
							// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
							if isFork {
								Warnf("Skipping fork %s", repo.GetFullName())
								continue RepoLoop
							}

							repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
						}
					}

					toBeFollowed := repoURLs
					cache, err := client.GetFollowedCache()
					hasCache := err == nil && cache != nil
					if !hasCache {
						if ignoreFollowedErrors {
							Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
						} else {
							panic(err)
						}
					} else {
						// Exclude already-followed projects:
						toBeFollowed = cache.RemoveFollowed(repoURLs)
					}
					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile(c.String("output"), "follow-by-meta-search", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// Follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// if the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
			},
			{
				Name:  "follow-by-code-search",
				Usage: "Follow projects by custom search on repositories code.",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max number of code results.",
					},
					&cli.BoolFlag{
						Name:  "force, y",
						Usage: "Don't ask for confirmation.",
					},
					&cli.StringFlag{
						Name:  "output, o",
						Usage: "File name to write repositories to.",
					},
				},
				Action: func(c *cli.Context) error {

					query := c.Args().First()
					if query == "" {
						Fataln("Must provide a query string")
					}
					limit := c.Int("limit")
					force := c.Bool("y")

					repoURLs := make([]string, 0)
					{
						Debugf("Getting list of repos for search: %s ...", ShakespeareBG(query))
						repos, err := GithubListReposByCodeSearch(query, limit)
						if err != nil {
							Fatalf("error while getting repo list for search %q: %s", query, err)
						}

						Debugf("Search %s has returned %v repos", ShakespeareBG(query), len(repos))
					RepoLoop:
						for _, repo := range repos {
							//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
							isFork := repo.GetFork()
							// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
							if isFork {
								Warnf("Skipping fork %s", repo.GetFullName())
								continue RepoLoop
							}

							repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
						}
					}

					toBeFollowed := repoURLs
					cache, err := client.GetFollowedCache()
					hasCache := err == nil && cache != nil
					if !hasCache {
						if ignoreFollowedErrors {
							Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
						} else {
							panic(err)
						}
					} else {
						// Exclude already-followed projects:
						toBeFollowed = cache.RemoveFollowed(repoURLs)
					}
					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile(c.String("output"), "follow-by-code-search", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// Follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// If the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								time.Sleep(waitDuration)
							}
						}
					}

					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
			},
			{
				Name:  "query",
				Usage: "Run a query on one or multiple projects.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "exclude, e",
						Usage: "Exclude project; example: github/api",
					},
					&cli.StringSliceFlag{
						Name:  "list-key, lk",
						Usage: "Project list key on which to run the query (can specify multiple).",
					},
					&cli.StringSliceFlag{
						Name:  "list",
						Usage: "Project list name on which to run the query (can specify multiple).",
					},
					&cli.StringFlag{
						Name:  "lang, l",
						Usage: "Language of the query project.",
					},
					&cli.StringFlag{
						Name:  "query, q",
						Usage: "Filepath to .ql query file.",
					},
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
					&cli.BoolFlag{
						Name:  "all, a",
						Usage: "Query all followed projects.",
					},
				},
				Action: func(c *cli.Context) error {

					lang := c.String("lang")
					if lang == "" {
						panic("--lang not set")
					}

					queryFilepath := c.String("query")
					if lang == "" {
						panic("--query not set")
					}

					fileExt := filepath.Ext(queryFilepath)
					if fileExt != ".ql" {
						Fatalf("file is not a .ql: %s", queryFilepath)
					}

					queryBytes, err := ioutil.ReadFile(queryFilepath)
					if err != nil {
						return err
					}
					queryString := string(queryBytes)

					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						repoListFilepaths := c.StringSlice("f")
						repoURLsRaw = append(repoURLsRaw, mustLoadTargetsFromFilepaths(repoListFilepaths...)...)
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLs := make([]string, 0)
					for _, raw := range repoURLsRaw {
						owner, isWholeUser, err := IsUserOnly(raw)
						if err != nil {
							panic(err)
						}
						if isWholeUser {
							Debugf("Getting list of repos for %s ...", owner)
							repos, err := GithubGetRepoList(owner)
							if err != nil {
								panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
							}
							Debugf("%s has %v repos", owner, len(repos))
							for _, repo := range repos {
								//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
								isFork := repo.GetFork()
								// "Currently we do not support analysis of forks. Consider adding the parent of the fork instead."
								if !isFork {
									repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
								} else {
									Warnf("Skipping fork %s", repo.GetFullName())
								}
							}
						} else {
							parsed, err := ParseGitURL(raw, false)
							if err != nil {
								panic(err)
							}
							repoURLs = append(repoURLs, parsed.URL())
						}
					}

					projectkeys := make([]string, 0)
					if len(repoURLs) > 0 {
						cache, err := client.GetFollowedCache()
						hasCache := err == nil && cache != nil
						if !hasCache {
							if ignoreFollowedErrors {
								Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
							} else {
								panic(err)
							}
						}

						excluded := c.StringSlice("exclude")

						if hasCache {
							// With cache:

							// If no repos specified, and flag --all is true, then query all:
							if c.Bool("all") {
								Infof("Gonna query all %v projects", cache.NumProjects())
								for _, pr := range cache.Projects() {
									repoURLs = append(repoURLs, pr.ExternalURL.URL)
								}
							}
							repoURLs = Deduplicate(repoURLs)

							for _, repoURL := range repoURLs {
								isProto := cache.IsProto(repoURL)
								if isProto {
									Warnf("%s is proto; skipping", trimGithubPrefix(repoURL))
									continue
								}

								pr := cache.GetProject(repoURL)
								if pr == nil {
									Warnf("%s is not followed; skipping", trimGithubPrefix(repoURL))
								} else {
									isSupportedLanguageForProject := pr.SupportsLanguage(lang)
									if !isSupportedLanguageForProject {
										Warnf("%s does not have language %s; skipping", trimGithubPrefix(repoURL), lang)
									} else {
										isExcluded := SliceContains(excluded, pr.DisplayName)
										if isExcluded {
											Warnf("%s is excluded; skipping", trimGithubPrefix(repoURL))
										} else {
											projectkeys = append(projectkeys, pr.Key)
										}
									}
								}
							}
						} else {
							// If no cache available:
							for _, repoURL := range repoURLs {
								if isGlob(repoURL) {
									// Skip because not a complete URL.
									Infof("Skipping %s", repoURL)
									continue
								}
								parsed, err := ParseGitURL(repoURL, true)
								if err != nil {
									panic(err)
								}
								isWholeUser := parsed.Repo == ""
								if isWholeUser {
									// Skip because not a complete URL.
									Infof("Skipping %s", repoURL)
									continue
								}

								pr, err := client.GetProjectBySlug(parsed.Slug())
								if err != nil {
									if isStatusResponseError(err) && err.(*StatusResponse).IsNotFound() {
										Warnf(
											"Project %s is not a built project.",
											trimGithubPrefix(repoURL),
										)
									} else {
										// General error
										panic(err)
									}
								} else {
									isSupportedLanguageForProject := pr.SupportsLanguage(lang)
									if !isSupportedLanguageForProject {
										Warnf("%s does not have language %s; skipping", trimGithubPrefix(repoURL), lang)
									} else {
										isExcluded := SliceContains(excluded, pr.DisplayName)
										if isExcluded {
											Warnf("%s is excluded; skipping", trimGithubPrefix(repoURL))
										} else {
											projectkeys = append(projectkeys, pr.Key)
										}
									}
								}
							}
						}
					}

					projectListKeys := c.StringSlice("list-key")
					projectListNames := c.StringSlice("list")

					if len(projectListNames) > 0 {
						// Add project lists by name:
						lists, err := client.ListProjectSelections()
						if err != nil {
							panic(err)
						}

						for _, name := range projectListNames {
							list := lists.ByName(name)
							if list == nil {
								Warnf("List %q not found; skipping", name)
							} else {
								projectListKeys = append(projectListKeys, list.Key)
							}
						}
					}

					yes, err := CLIAskYesNo(Sf(
						"Do you want to send the query %q to be run on %v projects and %v lists?",
						queryFilepath,
						len(projectkeys),
						len(projectListKeys),
					))
					if err != nil {
						panic(err)
					}
					if !yes {
						Infof("Aborting...")
						return nil
					}

					Infof(
						"Sending query %q to be run on %v projects and %v lists...",
						queryFilepath,
						len(projectkeys),
						len(projectListKeys),
					)
					queryConfig := &QueryConfig{
						Lang:                 lang,
						ProjectKeys:          projectkeys,
						QueryString:          queryString,
						ProjectSelectionKeys: projectListKeys,
					}
					resp, err := client.Query(queryConfig)
					if err != nil {
						return err
					}

					Infof("See query results at:")
					fmt.Println(resp.GetResultLink())
					return nil
				},
			},
			{
				Name:  "rebuild-proto",
				Usage: "(Re)build followed proto-projects.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "exclude, e",
						Usage: "Exclude project(s) by glob; example: github/api",
					},
					&cli.BoolFlag{
						Name:  "force, F",
						Usage: "Rebuild all proto-projects without asking confirmation for each.",
					},
				},
				Action: func(c *cli.Context) error {

					took := NewTimer()
					Infof("Getting list of followed proto-projects...")
					_, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently you're following %v proto-projects; took %s", len(protoProjects), took())

					force := c.Bool("F")

					excluded := c.StringSlice("exclude")

				RebuildLoop:
					for _, pr := range protoProjects {
						pattern, isBlacklisted := HasMatch(pr.DisplayName, excluded)
						if isBlacklisted && pattern != "" {
							Warnf(
								"%s is excluded (by pattern %q); skipping",
								pr.DisplayName,
								pattern,
							)
							continue RebuildLoop
						}

						var rebuildOrNot bool
						if !force {
							message := Sf(
								"%s is a proto-project; want to force new build attempt?",
								pr.DisplayName,
							)

							if pr.NextBuildStarted {
								message = Sf(
									"%s is a proto-project with a build attempt in progress; want to force a new build attempt?",
									pr.DisplayName,
								)
							}
							rebuildOrNot, err = CLIAskYesNo(message)
							if err != nil {
								return err
							}
						}

						doRebuild := force || rebuildOrNot

						if doRebuild {
							Infof(
								"Trying to issue a new build attempt for %s ...",
								pr.DisplayName,
							)
							err := client.RebuildProtoProject(pr.Key)
							if err != nil {
								Errorf(
									"Failed to start a new build attemp for %s: %s",
									pr.DisplayName,
									err,
								)
							} else {
								// sleep:
								time.Sleep(waitDuration)
							}
						}

					}

					return nil
				},
			},
			{
				Name:  "rebuild",
				Usage: "Rebuild followed projects.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "exclude, e",
						Usage: "Exclude project(s) by glob; example: github/api",
					},
					&cli.StringFlag{
						Name:  "lang, l",
						Usage: "Language of the query project.",
					},
					&cli.BoolFlag{
						Name:  "force, F",
						Usage: "Rebuild without asking for confirmation.",
					},
					&cli.BoolFlag{
						Name:  "all",
						Usage: "Rebuild all projects for specific language.",
					},
				},
				Action: func(c *cli.Context) error {

					lang := c.String("lang")
					if lang == "" {
						panic("--lang not set")
					}

					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently you're following %v projects (and %v proto-projects); took %s", len(projects), len(protoProjects), took())

					var projectsThatSupportTheLanguage int
					for _, pr := range projects {
						isSupportedLanguageForProject := pr.SupportsLanguage(lang)
						if isSupportedLanguageForProject {
							projectsThatSupportTheLanguage++
						}
					}
					Infof(
						ShakespeareBG("%v/%v projects support the %s language (%v do not)"),
						projectsThatSupportTheLanguage,
						len(projects),
						lang,
						len(projects)-projectsThatSupportTheLanguage,
					)

					force := c.Bool("F")
					rebuildAll := c.Bool("all")

					excluded := c.StringSlice("exclude")

				RebuildLoop:
					for _, pr := range projects {
						pattern, isBlacklisted := HasMatch(pr.DisplayName, excluded)
						if isBlacklisted && pattern != "" {
							Warnf(
								"%s is excluded (by pattern %q); skipping",
								pr.DisplayName,
								pattern,
							)
							continue RebuildLoop
						}

						isSupportedLanguageForProject := pr.SupportsLanguage(lang)

						// Rebuild if a project does not support the specified language.
						if !isSupportedLanguageForProject {
							Infof(
								"%s does NOT have language %s; starting new build attempt ...",
								pr.DisplayName,
								lang,
							)
							err := client.NewBuildAttempt(pr.Key, lang)
							if err != nil {
								Errorf(
									"Failed to issue a new build attemp for %s for %s language: %s",
									pr.DisplayName,
									lang,
									err,
								)
							} else {
								// sleep:
								time.Sleep(waitDuration)
							}
						}

						if isSupportedLanguageForProject && rebuildAll {
							var rebuildOrNot bool
							if !force {
								rebuildOrNot, err = CLIAskYesNo(Sf(
									"%s does already have language %s; Want to force new build attempt?",
									pr.DisplayName,
									lang,
								))
								if err != nil {
									return err
								}
							}

							doRebuild := force || rebuildOrNot

							if doRebuild {
								Infof(
									"Trying to issue a new test rebuild for %s for %s language ...",
									pr.DisplayName,
									lang,
								)
								err := client.RequestTestBuild(pr.Slug, lang)
								if err != nil {
									Errorf(
										"Failed to start a new test build attemp for %s for %s language: %s",
										pr.DisplayName,
										lang,
										err,
									)
								} else {
									// sleep:
									time.Sleep(waitDuration)
								}
							}
						}

					}

					return nil
				},
			},
			{
				Name:  "followed",
				Usage: "List all followed projects.",
				Flags: []cli.Flag{},
				Action: func(c *cli.Context) error {

					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof(
						"%v projects and %v proto-projects; took %s",
						len(projects),
						len(protoProjects),
						took(),
					)

					for _, proto := range protoProjects {
						Sfln("%s", proto.CloneURL)
					}
					for _, pr := range projects {
						Sfln("%s", pr.ExternalURL.URL)
					}

					return nil
				},
			},
			{
				Name:  "lists",
				Usage: "List all lists of projects.",
				Flags: []cli.Flag{},
				Action: func(c *cli.Context) error {

					took := NewTimer()
					Infof("Getting list of lists...")
					lists, err := client.ListProjectSelections()
					if err != nil {
						panic(err)
					}
					Infof("%v lists; took %s", len(lists), took())

					sort.Slice(lists, func(i, j int) bool {
						return lists[i].Name < lists[j].Name
					})
					Errorln(Bold("NAME | KEY"))
					for _, list := range lists {
						Sfln(
							"%s | %s",
							list.Name,
							list.Key,
						)
					}

					return nil
				},
			},
			{
				Name:  "create-list",
				Usage: "Create a new list.",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "Name of the list to be created.",
					},
				},
				Action: func(c *cli.Context) error {

					name := c.Args().First()
					if name == "" {
						return errors.New("name not provided")
					}

					took := NewTimer()
					Infof("Creating new list with name %q...", name)
					err := client.CreateProjectSelection(name)
					if err != nil {
						panic(err)
					}
					Infof(
						"Created new list %q; took %s",
						name,
						took(),
					)

					return nil
				},
			},
			{
				Name:  "delete-list",
				Usage: "Delete a list.",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "Name of the list to be deleted.",
					},
				},
				Action: func(c *cli.Context) error {

					name := c.Args().First()
					if name == "" {
						return errors.New("name not provided")
					}

					took := NewTimer()
					Infof("Deleting list with name %q...", name)
					err := client.DeleteProjectSelection(name)
					if err != nil {
						panic(err)
					}
					Infof(
						"Deleted list %q; took %s",
						name,
						took(),
					)

					return nil
				},
			},
			{
				Name:  "list",
				Usage: "List projects inside a list by its name.",
				Flags: []cli.Flag{},
				Action: func(c *cli.Context) error {

					name := c.Args().First()
					if name == "" {
						return errors.New("name not provided")
					}

					took := NewTimer()
					Infof("Getting projects of %q list...", name)
					resp, err := client.ListProjectsInSelection(name)
					if err != nil {
						panic(err)
					}
					Infof(
						"List contains %v projects; took %s",
						len(resp.ProjectKeys),
						took(),
					)

					projectCount := len(resp.ProjectKeys)
					partsNumber := calcChunkCount(projectCount, 100)

					chunks := SplitStringSlice(partsNumber, resp.ProjectKeys)

					for chunkIndex, chunk := range chunks {
						Infof(
							"Getting list %q; chunk %v/%v...",
							name,
							chunkIndex+1,
							len(chunks),
						)
						took = NewTimer()
						gotProjectResp, err := client.GetProjectsByKey(chunk...)
						if err != nil {
							Errorf(
								"error while client.GetProjectsByKey for projects %s: %s",
								resp.ProjectKeys,
								err,
							)
						}
						Infof("took %s", took())

						for _, pr := range gotProjectResp.FullProjects {
							Sfln(
								"%s",
								pr.ExternalURL.URL,
							)
						}
					}

					return nil
				},
			},
			{
				Name:  "add-to-list",
				Usage: "Add followed projects to a list.",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "name",
						Usage: "Name of the list to which add the projects (can use multiple times).",
					},
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
				},
				Action: func(c *cli.Context) error {

					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						repoListFilepaths := c.StringSlice("f")
						repoURLsRaw = append(repoURLsRaw, mustLoadTargetsFromFilepaths(repoListFilepaths...)...)
					}
					repoURLsRaw = Deduplicate(repoURLsRaw)

					repoURLs := make([]string, 0)
					for _, raw := range repoURLsRaw {
						owner, isWholeUser, err := IsUserOnly(raw)
						if err != nil {
							panic(err)
						}
						if isWholeUser {
							Debugf("Getting list of repos for %s ...", owner)
							repos, err := GithubGetRepoList(owner)
							if err != nil {
								panic(fmt.Errorf("error while getting repo list for user %q: %s", owner, err))
							}
							Debugf("%s has %v repos", owner, len(repos))
							for _, repo := range repos {
								//repoURLs = append(repoURLs, repo.GetFullName()) // e.g. "kubernetes/dashboard"
								repoURLs = append(repoURLs, repo.GetHTMLURL()) // e.g. "https://github.com/kubernetes/dashboard"
							}
						} else {
							parsed, err := ParseGitURL(raw, false)
							if err != nil {
								panic(err)
							}
							repoURLs = append(repoURLs, parsed.URL())
						}
					}

					alreadyFollowedProjectKeys := make(map[string][]string, 0)

					listNames := c.StringSlice("name")
					lists, err := client.ListProjectSelections()
					if err != nil {
						panic(err)
					}

					// Check if all lists exist;
					// if a list does NOT exist, ask if want it to be created:
					for _, wantedListName := range listNames {
						exists := lists.ByName(wantedListName) != nil
						if !exists {
							Warnf("The %q list does not exist.", wantedListName)
							yes, err := CLIAskYesNo(Sf("Do you want to create %q list?", wantedListName))
							if err != nil {
								return err
							}
							if yes {
								// Create the new list:
								took := NewTimer()
								err := client.CreateProjectSelection(wantedListName)
								if err != nil {
									panic(err)
								}
								Infof(
									"Created new list %q; took %s",
									wantedListName,
									took(),
								)
							}
						} else {
							// Get list of projects inside the list, and cache them:
							took := NewTimer()
							Infof("Getting projects of %q list...", wantedListName)
							resp, err := client.ListProjectsInSelection(wantedListName)
							if err != nil {
								panic(err)
							}
							Infof("took %s", took())
							alreadyFollowedProjectKeys[wantedListName] = resp.ProjectKeys
						}
					}
					{ // Refresh list of selections:
						lists, err = client.ListProjectSelections()
						if err != nil {
							panic(err)
						}
					}

					cache, err := client.GetFollowedCache()
					hasCache := err == nil && cache != nil
					if !hasCache {
						if ignoreFollowedErrors {
							Warnf("Could not load list of followed projects. Continuing without list of followed projects.")
						} else {
							panic(err)
						}
					}

					projectKeys := make([]string, 0)
					for _, repoURL := range repoURLs {
						// Only built projects can be added to a list.
						// try to find out whether it is a built project or not:
						var isABuiltProject bool
						if hasCache {
							// If succeeded to get the list of followed projects,
							// then check whether the project is present there.
							// NOTE: Even if it is not a followed project, it still could be a built project.
							pr := cache.GetProject(repoURL)
							if pr != nil {
								isABuiltProject = true
								projectKeys = append(projectKeys, pr.Key)
							}
						}
						if !isABuiltProject {
							parsed, err := ParseGitURL(repoURL, true)
							if err != nil {
								panic(err)
							}
							pr, err := client.GetProjectBySlug(parsed.Slug())
							if err != nil {
								if isStatusResponseError(err) && err.(*StatusResponse).IsNotFound() {
									Warnf(
										"Project %s is not a built project; cannot be added to list.",
										trimGithubPrefix(repoURL),
									)
								} else {
									// General error
									panic(err)
								}
							} else {
								isABuiltProject = true
								projectKeys = append(projectKeys, pr.Key)
							}
						}
					}

					{
						for _, wantedListName := range listNames {
							// Add to one list at a time:
							list := lists.ByName(wantedListName)
							if list == nil {
								continue
							}
							addedCount := 0

							notFollowedByThisList := Filter(projectKeys,
								func(i int, prKey string) bool {
									notFollowed := !SliceContains(alreadyFollowedProjectKeys[wantedListName], prKey)
									return notFollowed
								}).([]string)

							partsNumber := calcChunkCount(len(notFollowedByThisList), 100)
							chunks := SplitStringSlice(partsNumber, notFollowedByThisList)
							for chunkIndex, chunk := range chunks {
								Infof(
									"Adding projects to %q list; chunk %v/%v...",
									list.Name,
									chunkIndex+1,
									len(chunks),
								)
								addedCount += len(chunk)
								err = client.AddProjectToSelection(list.Key, chunk...)
								if err != nil {
									panic(err)
								}
							}
							Infof("Added %v new projects to %q list.", addedCount, wantedListName)
						}
					}

					return nil
				},
			},
			{
				Name:  "x-list-query-results",
				Usage: "[x] List projects of a query run (json).",
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "min-alerts",
						Usage: "Min number of alerts; will sort by alert count.",
					},
					&cli.IntFlag{
						Name:  "min-results",
						Usage: "Min number of results; will sort by result count.",
					},
				},
				Action: func(c *cli.Context) error {

					queryID := c.Args().First()
					if queryID == "" {
						return errors.New("query ID not provided")
					}
					minAlerts := c.Int("min-alerts")
					minResults := c.Int("min-results")
					if minAlerts > 0 && minResults > 0 {
						return errors.New("Cannot use both: min-alerts and min-results")
					}

					var orderBy OrderBy
					if minAlerts > 0 {
						orderBy = OrderByNumAlerts
					}
					if minResults > 0 {
						orderBy = OrderByNumResults
					}
					if minAlerts == 0 && minResults == 0 {
						orderBy = OrderByNumResults
					}

					took := NewTimer()
					Infof("Getting results of query %s...", queryID)

					var startCursor string
					queryResults := make([]*GetQueryResultsResponseItem, 0)
				GetterLoop:
					for {
						resp, err := client.GetQueryResults(queryID, orderBy, startCursor)
						if err != nil {
							panic(err)
						}
						if resp.Items == nil {
							break GetterLoop
						}

						for _, item := range resp.Items {
							{
								if minAlerts > 0 && item.Stats == nil {
									continue
								}
								if minAlerts > 0 && item.Stats.NumAlerts < minAlerts {
									break GetterLoop
								}
							}
							{
								if minResults > 0 && item.Stats == nil {
									continue
								}
								if minResults > 0 && item.Stats.NumResults < minResults {
									break GetterLoop
								}
							}
							queryResults = append(queryResults, item)
						}
						if resp.Cursor == "" {
							break GetterLoop
						}
						startCursor = resp.Cursor
					}
					Infof(
						"Got %v results; took %s",
						len(queryResults),
						took(),
					)

					projectCount := len(queryResults)
					partsNumber := calcChunkCount(projectCount, 100)

					projectKeys := MapSlice(queryResults, func(i int) string {
						return queryResults[i].ProjectKey
					})

					chunks := SplitStringSlice(partsNumber, projectKeys)

					type Output struct {
						Project *Project
						Result  *GetQueryResultsResponseItem
					}
					output := make([]*Output, 0)
					for chunkIndex, chunk := range chunks {
						Infof(
							"Getting projects' meta; chunk %v/%v...",
							chunkIndex+1,
							len(chunks),
						)
						took = NewTimer()
						gotProjectResp, err := client.GetProjectsByKey(chunk...)
						if err != nil {
							Fatalf(
								"error while client.GetProjectsByKey for projects %s: %s",
								projectKeys,
								err,
							)
						}
						Infof("took %s", took())

						for projectKey, pr := range gotProjectResp.FullProjects {
							out := &Output{
								Project: pr,
							}

							{
								got := FilterSlice(queryResults, func(i int) bool {
									return queryResults[i].ProjectKey == projectKey
								}).([]*GetQueryResultsResponseItem)
								out.Result = got[0]
							}
							output = append(output, out)
						}
					}

					js, err := json.Marshal(output)
					if err != nil {
						Fatalf("Error marshaling results to json: %s", err)
					}

					Ln(string(js))

					return nil
				},
			},
		},
	}

	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(app.Commands))

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
func GithubListLanguages(owner string, repo string) ([]string, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)

	languagesMap, err := ghClient.ListLanguagesOfRepo(owner, repo)
	if err != nil {
		return nil, err
	}

	var languages []string

	for key := range languagesMap {
		languages = append(languages, ToLower(key))
	}

	languages = Deduplicate(languages)
	return languages, nil
}
func GithubListReposByLanguage(owner string, lang string) ([]*github.Repository, error) {
	owner = strings.TrimSpace(owner)
	lang = strings.TrimSpace(lang)

	repos, err := ghClient.ListReposBylanguage(owner, lang)
	if err != nil {
		return nil, err
	}

	return repos, nil
}
func GithubListAllReposByLanguage(lang string, limit int) ([]*github.Repository, error) {
	lang = strings.TrimSpace(lang)

	opts := &ghc.ListAllReposByLanguageOpts{
		Language:     lang,
		ExcludeForks: true,
		Limit:        limit,
	}
	repos, err := ghClient.ListAllReposByLanguage(opts)
	if err != nil {
		return nil, err
	}

	return repos, nil
}
func GithubListReposByMetaSearch(query string, limit int) ([]*github.Repository, error) {
	opts := &ghc.SearchReposOpts{
		Query: query,
		Limit: limit,
	}
	return ghClient.SearchRepos(opts)
}
func GithubListReposByCodeSearch(query string, limit int) ([]*github.Repository, error) {
	opts := &ghc.SearchCodeOpts{
		Query: query,
		Limit: limit,
	}
	codeResults, err := ghClient.SearchCode(opts)
	if err != nil {
		return nil, err
	}

	// Deduplicate results (for any given repo, there might be more than one code results).
	DeduplicateSlice2(&codeResults, func(i int) string {
		return codeResults[i].Repository.GetHTMLURL()
	})

	var repos []*github.Repository
	for _, codeResult := range codeResults {
		repos = append(repos, codeResult.Repository)
	}

	return repos, nil
}
func GithubGetRepoList(owner string) ([]*github.Repository, error) {

	owner = strings.TrimSpace(owner)

	// determine whether the owner is a user or an org:
	ownerUser, isUser, err := ghClient.IsOwnerAUser(owner)
	if err != nil {
		return nil, bianconiglio.Contextualize(err,
			"owner", owner,
		)
	}

	var ownerOrg *github.Organization
	var isOrg bool
	if !isUser {
		ownerOrg, isOrg, err = ghClient.IsOwnerAnOrg(owner)
		if err != nil {
			return nil, bianconiglio.Contextualize(err,
				"owner", owner,
			)
		}
		if !isOrg {
			return nil, fmt.Errorf("owner is neither an org nor a common user: %s", owner)
		}
	}

	IsUser := func() bool {
		return ownerUser != nil && ownerUser.GetType() != "Organization"
	}
	_ = IsUser

	IsOrg := func() bool {
		return ownerOrg != nil || (ownerUser != nil && ownerUser.GetType() == "Organization")
	}

	if ownerUser == nil && ownerOrg == nil {
		return nil, bianconiglio.Contextualize(
			errors.New("ownerUser and ownerOrg are both nil"),
			"owner", owner,
		)
	}

	// get list of repos:
	_ = IsOrg()

	repoList := make([]*github.Repository, 0)

	{ // get list of repos:
		if isOrg {
			orgRepos, err := ghClient.ListReposByOrg(owner)
			if err != nil {
				return nil, fmt.Errorf("error while ListReposByOrg: %s", err)
			}
			repoList = append(repoList, orgRepos...)
		} else {
			userRepos, err := ghClient.ListReposByUser(owner)
			if err != nil {
				return nil, fmt.Errorf("error while ListReposByUser: %s", err)
			}
			repoList = append(repoList, userRepos...)
		}
	}

	return repoList, nil
}

func LoadConfigFromFile(filepath string) (*Config, error) {
	jsonFile, err := ioutil.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("error while reading config file from %q: %s", filepath, err)
	}

	var conf Config
	err = json.Unmarshal(jsonFile, &conf)
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling config file: %s", err)
	}

	return &conf, nil
}

type LGTMSession struct {
	Nonce        string `json:"nonce"`
	ShortSession string `json:"short_session"`
	LongSession  string `json:"long_session"`
}

// Validate validates
func (sess *LGTMSession) Validate() error {
	if sess.Nonce == "" {
		return errors.New("session.nonce is not set")
	}
	if sess.ShortSession == "" {
		return errors.New("session.short_session is not set")
	}
	if sess.LongSession == "" {
		return errors.New("session.long_session is not set")
	}
	return nil
}

type Config struct {
	APIVersion string        `json:"api_version"`
	Session    *LGTMSession  `json:"session,omitempty"`
	GitHub     *GithubConfig `json:"github,omitempty"`
}

type GithubConfig struct {
	Token string `json:"token"`
}

// Validate validates
func (conf *Config) Validate() error {
	if conf.APIVersion == "" {
		return errors.New("conf.api_version is not set")
	}
	if conf.Session == nil {
		return errors.New("conf.session is not set")
	}
	if err := conf.Session.Validate(); err != nil {
		return fmt.Errorf("error while validating conf.session: %s", err)
	}
	if conf.GitHub == nil {
		return errors.New("conf.github is not set")
	}
	if conf.GitHub.Token == "" {
		return errors.New("conf.github.token is not set")
	}
	return nil
}

func HasPrefix(s string, prefix string) bool {
	return strings.HasPrefix(s, prefix)
}
func IsEmptyHostError(err error) bool {
	if e, ok := err.(*url.Error); ok && e.Err.Error() == "empty host" {
		return true
	}
	return false
}

// TrimSlashes trims initial and final slashes.
func TrimSlashes(s string) string {
	return strings.Trim(s, "/")
}

// IsUserOnly returns a bool telling whether only the user is specified (i.e. whole account, without a particular repo name).
func IsUserOnly(rawURL string) (string, bool, error) {
	grl, err := ParseGitURL(rawURL, false)
	if err != nil {
		return "", false, err
	}

	isWholeUser := grl.Repo == ""
	if isWholeUser {
		return grl.User, isWholeUser, nil
	}
	return "", false, nil
}

type GitURL struct {
	Scheme   string
	Hostname string
	Port     string

	User string
	Repo string
}

func (grl *GitURL) Slug() string {
	switch grl.Hostname {
	case "github.com":
		return Sf(
			"g/%s/%s",
			grl.User,
			grl.Repo,
		)
	case "gitlab.com":
		return Sf(
			"gl/%s/%s",
			grl.User,
			grl.Repo,
		)
	case "bitbucket.org":
		return Sf(
			"b/%s/%s",
			grl.User,
			grl.Repo,
		)
	default:
		panic(Sf("no known slug prefix for %s", grl.Hostname))
	}
}
func (grl *GitURL) URL() string {
	if grl.Port != "" {
		if grl.Repo != "" {
			return grl.Scheme + "://" + grl.Hostname + ":" + grl.Port + "/" + grl.User + "/" + grl.Repo
		}
		return grl.Scheme + "://" + grl.Hostname + ":" + grl.Port + "/" + grl.User
	} else {
		if grl.Repo != "" {
			return grl.Scheme + "://" + grl.Hostname + "/" + grl.User + "/" + grl.Repo
		}
		return grl.Scheme + "://" + grl.Hostname + "/" + grl.User
	}
}

// ParseGitURL verifies and splits a URL into the git repo info (hostname, userr account name, repo name)
func ParseGitURL(rawURL string, mustHaveRepoName bool) (*GitURL, error) {
	//rawURL = TrimSlashes(rawURL)
	rawURL = strings.TrimSuffix(rawURL, ".git")
	{
		if CountSlashes(rawURL) == 1 || CountSlashes(rawURL) == 0 {
			rawURL = TrimSlashes(defaultHost) + "/" + TrimSlashes(rawURL)
		}
	}
	parsedURL, err := urlx.ParseWithDefaultScheme(rawURL, "https")
	if err != nil {
		return nil, err
	}

	final := &GitURL{}

	final.Scheme = parsedURL.Scheme
	final.Hostname = SanitizeFileNamePart(parsedURL.Hostname())
	final.Port = parsedURL.Port()

	path := TrimSlashes(parsedURL.Path)

	slashCount := strings.Count(path, "/")

	if !mustHaveRepoName {
		if slashCount > 1 {
			return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
		}

		if slashCount > 0 {
			slice := strings.Split(path, "/")
			if len(slice) < 1 {
				return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
			}
			final.User = SanitizeFileNamePart(strings.TrimSpace(slice[0]))
			if len(slice) > 1 {
				final.Repo = SanitizeFileNamePart(strings.TrimSpace(slice[1]))
			}
		}

		if slashCount == 0 {
			final.User = SanitizeFileNamePart(path)
		}

	} else {
		if slashCount != 1 {
			return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
		}

		slice := strings.Split(path, "/")
		if len(slice) != 2 {
			return nil, fmt.Errorf("invalid URL: %s contains a wrong number of slashes", path)
		}
		final.User = SanitizeFileNamePart(strings.TrimSpace(slice[0]))
		final.Repo = SanitizeFileNamePart(strings.TrimSpace(slice[1]))
	}

	if len(final.User) == 0 {
		return nil, errors.New("user not specified")
	}
	if len(final.Repo) == 0 && mustHaveRepoName {
		return nil, errors.New("repo not specified")
	}

	return final, nil
}
func CountSlashes(s string) int {
	return strings.Count(s, "/")
}

func trimGithubPrefix(s string) string {
	return strings.TrimPrefix(s, "https://github.com/")
}

func saveTargetListToTempFile(outputFileName string, cmdName string, targets []string) {
	var outputFile *os.File
	var err error

	if outputFileName == "" {
		scanName := Sf(
			"lgtml-cli-%s-%s",
			cmdName,
			time.Now().Format(FilenameTimeFormat),
		)
		outputFile, err = ioutil.TempFile("", scanName+".*.txt")
		outputFileName = outputFile.Name()
	} else {
		outputFile, err = os.Create(outputFileName)
	}

	if err != nil {
		log.Fatal(err)
	}

	writer := bufio.NewWriter(outputFile)

	for _, target := range targets {
		_, err := writer.WriteString(target + "\n")
		if err != nil {
			outputFile.Close()
			log.Fatal(err)
		}
	}

	if err := writer.Flush(); err != nil {
		log.Fatal(err)
	}

	fmt.Println(Sf(PurpleBG("Wrote compiled list of targets to temp file %s"), outputFileName))

	if err := outputFile.Close(); err != nil {
		log.Fatal(err)
	}
}

func isGlob(s string) bool {
	return strings.Contains(s, "*")
}

// getGlobsThatMatchEverything returns all patterns that match
// any repo.
func getGlobsThatMatchEverything(patterns []string) []string {
	var res []string
	for _, pattern := range patterns {
		if strings.HasSuffix(pattern, "/*/*") || strings.HasSuffix(pattern, "github.com/*") {
			res = append(res, pattern)
		}
	}
	return res
}
func isAlreadyFollowedProject(projects []*Project, projectURL string) (*Project, bool) {
	for _, pr := range projects {
		alreadyFollowed := ToLower(projectURL) == ToLower(pr.ExternalURL.URL)
		if alreadyFollowed {
			return pr, true
		}
	}
	return nil, false
}

func isAlreadyFollowedProto(protoProjects []*ProtoProject, projectURL string) (*ProtoProject, bool) {
	for _, pr := range protoProjects {
		alreadyFollowed := isProtoMatch(pr.CloneURL, projectURL)
		if alreadyFollowed {
			return pr, true
		}
	}
	return nil, false
}

func isProtoMatch(cloneURL string, projectURL string) bool {
	withDotGitSuffix := ""
	if !strings.HasSuffix(projectURL, ".git") {
		withDotGitSuffix = projectURL + ".git"
	} else {
		withDotGitSuffix = projectURL
	}
	alreadyFollowed := (ToLower(projectURL) == ToLower(cloneURL)) || (ToLower(withDotGitSuffix) == ToLower(cloneURL))
	return alreadyFollowed
}

type FollowedProjectCache struct {
	mu       *sync.RWMutex
	projects []*Project
	proto    []*ProtoProject
	client   *Client
}

//
func (fpc *FollowedProjectCache) IsFollowed(repoURL string) bool {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	_, isFollowed := isAlreadyFollowedProject(fpc.projects, repoURL)
	_, isFollowedProto := isAlreadyFollowedProto(fpc.proto, repoURL)
	return isFollowed || isFollowedProto
}

// Has returns true if the project/proto-project is followed.
func (fpc *FollowedProjectCache) HasAny(repoURL string) bool {
	return fpc.IsFollowed(repoURL)
}

// Get returns a Project if it is present in the followed projects cache.
func (fpc *FollowedProjectCache) GetProject(repoURL string) *Project {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	pr, isFollowed := isAlreadyFollowedProject(fpc.projects, repoURL)
	if isFollowed && pr != nil {
		return pr
	}
	return nil
}

// GetProto returns a ProtoProject if it is present in the followed proto-projects cache.
func (fpc *FollowedProjectCache) GetProto(repoURL string) *ProtoProject {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	pr, isFollowedProto := isAlreadyFollowedProto(fpc.proto, repoURL)
	if isFollowedProto && pr != nil {
		return pr
	}
	return nil
}

//
func (fpc *FollowedProjectCache) IsProto(repoURL string) bool {
	pr := fpc.GetProto(repoURL)
	return pr != nil
}

//
func (fpc *FollowedProjectCache) Refresh() error {
	took := NewTimer()
	Infof("Getting list of followed projects...")
	projects, protoProjects, err := fpc.client.ListFollowedProjects()
	if err != nil {
		return fmt.Errorf("error while getting list of followed projects: %s", err)
	}
	Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

	fpc.mu.Lock()
	defer fpc.mu.Unlock()
	fpc.projects = projects
	fpc.proto = protoProjects

	return nil
}
func (fpc *FollowedProjectCache) RemoveFollowed(candidates []string) []string {
	toBeFollowed := Filter(candidates, func(i int, repoURL string) bool {
		isNOTFollowed := !fpc.HasAny(repoURL)
		return isNOTFollowed
	}).([]string)
	return Deduplicate(toBeFollowed)
}
func (fpc *FollowedProjectCache) NumProjects() int {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	return len(fpc.projects)
}
func (fpc *FollowedProjectCache) NumProto() int {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	return len(fpc.proto)
}
func (fpc *FollowedProjectCache) Projects() []*Project {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	return Filter(fpc.projects, func(i int) bool {
		return true
	}).([]*Project)
}
func (fpc *FollowedProjectCache) ProtoProjects() []*ProtoProject {
	fpc.mu.RLock()
	defer fpc.mu.RUnlock()

	return Filter(fpc.proto, func(i int) bool {
		return true
	}).([]*ProtoProject)
}
func (cl *Client) GetFollowedCache() (*FollowedProjectCache, error) {
	fpc := NewFollowedProjectCache(cl)
	err := fpc.Refresh()
	if err != nil {
		return nil, err
	}
	return fpc, nil
}

func NewFollowedProjectCache(cl *Client) *FollowedProjectCache {
	return &FollowedProjectCache{
		client: cl,
		mu:     &sync.RWMutex{},
	}
}

func calcChunkCount(total int, chunkSize int) int {
	partsNumber := total / chunkSize
	if total < chunkSize {
		partsNumber = 1
	} else {
		partsNumber++
	}
	return partsNumber
}

func trimDotGit(s string) string {
	return strings.TrimSuffix(s, ".git")
}
func mustLoadTargetsFromFilepaths(paths ...string) []string {
	var res []string
	for _, path := range paths {
		err := ReadConfigLinesAsString(path, func(line string) bool {
			res = append(res, line)
			return true
		})
		if err != nil {
			panic(err)
		}
	}
	return res
}
