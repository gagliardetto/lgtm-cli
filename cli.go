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
	"time"

	"github.com/gagliardetto/bianconiglio"
	"github.com/gagliardetto/eta"
	ghc "github.com/gagliardetto/gh-client"
	"github.com/gagliardetto/request"
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
	var waitDuration time.Duration
	var client *Client

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
		},
		Before: func(c *cli.Context) error {

			configFilepathFromEnv := os.Getenv("LGTM_CLI_CONFIG")

			if configFilepath == "" && configFilepathFromEnv == "" {
				Errorf("No config provided. Please specify the path to the config file with the LGTM_CLI_CONFIG env var.")
				return errors.New(c.App.Usage)
			}

			// if the flag is not set, use env variable:
			if configFilepath == "" {
				configFilepath = configFilepathFromEnv
			}

			conf, err := LoadConfigFromFile(configFilepath)
			if err != nil {
				panic(err)
			}
			if err := conf.Validate(); err != nil {
				Fatalf("Config is not valid: %s", err)
			}

			client, err = NewClient(conf)
			if err != nil {
				panic(err)
			}

			// setup github client:
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
			//IsExitingFunc = IsExiting
			return nil
		},
		Commands: []cli.Command{
			{
				Name:  "unfollow-all",
				Usage: "Unfollow all currently followed repositories (\"projects\".",
				Action: func(c *cli.Context) error {
					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())
					totalProjects := len(projects)
					totalProtoProjects := len(protoProjects)
					total := totalProjects + totalProtoProjects

					Infof("You are following %v projects", total)

					if total == 0 {
						return nil
					}
					Infof("Starting to unfollow all...")

					etac := eta.New(int64(total))

					for _, pr := range projects {
						unfollower(false, pr.Key, pr.ExternalURL.URL, etac)
					}
					for _, proto := range protoProjects {
						unfollower(true, proto.Key, proto.CloneURL, etac)
					}
					return nil
				},
			},
			{
				Name:  "unfollow",
				Usage: "Unfollow one or more projects.",
				Action: func(c *cli.Context) error {
					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						repoListFilepaths := c.StringSlice("f")
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
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
						Infof("The following patterns will match all followed projects, and consequently all followed projects will be unfollowed.")
						Infof("%s", Sq(matchAllPatterns))
						CLIMustConfirmYes("Do you really want to unfollow all repositories?")
					}

					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

					toBeUnfollowed := make([]*Project, 0)
					// match repos against list of repos followed:
					for _, pr := range projects {
						_, isToBeUnfollowed := HasMatch(pr.ExternalURL.URL, repoURLPatterns)
						if isToBeUnfollowed {
							toBeUnfollowed = append(toBeUnfollowed, pr)
						}
					}
					Infof("Will unfollow %v projects...", len(toBeUnfollowed))

					etac := eta.New(int64(len(toBeUnfollowed)))

					// unfollow projects:
					for _, pr := range toBeUnfollowed {
						message := pr.ExternalURL.URL

						pattern, matched := HasMatch(pr.ExternalURL.URL, repoURLPatterns)
						if matched {
							message += " " + Sf("(matched from %s pattern)", Lime(pattern))
						}
						unfollower(false, pr.Key, message, etac)
					}
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
				},
			},
			{
				Name:  "follow",
				Usage: "Follow one or more projects.",
				Action: func(c *cli.Context) error {

					lang := ToLower(c.String("lang"))

					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						repoListFilepaths := c.StringSlice("f")
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
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

					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

					toBeFollowed := make([]string, 0)
					// exclude already-followed projects:
					for _, repoURL := range repoURLs {
						_, isFollowed := isAlreadyFollowedProject(projects, repoURL)
						_, isFollowedProto := isAlreadyFollowedProto(protoProjects, repoURL)
						isNOTFollowed := !isFollowed && !isFollowedProto
						if isNOTFollowed {
							toBeFollowed = append(toBeFollowed, repoURL)
						}
					}
					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile("follow", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// if the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								// sleep:
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
					&cli.StringFlag{
						Name:  "lang, l",
						Usage: "Filter github repos by language.",
					},
				},
			},
			{
				Name:  "follow-by-lang",
				Usage: "Follow projects by language.",
				Action: func(c *cli.Context) error {

					lang := c.Args().First()
					if lang == "" {
						Fataln("must provide a language")
					}
					limit := c.Int("limit")
					start := c.Int("start")
					force := c.Bool("y")

					repoURLs := make([]string, 0)
					{
						Debugf("Getting list of repos for language: %s ...", lang)

						repos, err := GithubListAllReposByLanguage(lang, limit)
						if err != nil {
							panic(fmt.Errorf("error while getting repo list for language %q: %s", lang, err))
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
					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

					toBeFollowed := make([]string, 0)
					// exclude already-followed projects:
					for _, repoURL := range repoURLs {
						_, isFollowed := isAlreadyFollowedProject(projects, repoURL)
						_, isFollowedProto := isAlreadyFollowedProto(protoProjects, repoURL)
						isNOTFollowed := !isFollowed && !isFollowedProto
						if isNOTFollowed {
							toBeFollowed = append(toBeFollowed, repoURL)
						}
					}
					toBeFollowed = Deduplicate(toBeFollowed)
					totalToBeFollowed := len(toBeFollowed)

					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile("follow-by-lang", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// if the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								// sleep:
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
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
				},
			},
			{
				Name:  "follow-by-meta-search",
				Usage: "Follow projects by custom search on repositories meta.",
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
							panic(fmt.Errorf("error while getting repo list for search %q: %s", query, err))
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

					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

					toBeFollowed := make([]string, 0)
					// exclude already-followed projects:
					for _, repoURL := range repoURLs {
						_, isFollowed := isAlreadyFollowedProject(projects, repoURL)
						_, isFollowedProto := isAlreadyFollowedProto(protoProjects, repoURL)
						isNOTFollowed := !isFollowed && !isFollowedProto
						if isNOTFollowed {
							toBeFollowed = append(toBeFollowed, repoURL)
						}
					}
					toBeFollowed = Deduplicate(toBeFollowed)

					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile("follow-by-meta-search", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// if the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								// sleep:
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max number of projects to get and follow.",
					},
					&cli.BoolFlag{
						Name:  "force, y",
						Usage: "Don't ask for confirmation.",
					},
				},
			},
			{
				Name:  "follow-by-code-search",
				Usage: "Follow projects by custom search on repositories code.",
				Action: func(c *cli.Context) error {

					query := c.Args().First()
					if query == "" {
						Fataln("must provide a query string")
					}
					limit := c.Int("limit")
					force := c.Bool("y")

					repoURLs := make([]string, 0)
					{
						Debugf("Getting list of repos for search: %s ...", ShakespeareBG(query))
						repos, err := GithubListReposByCodeSearch(query, limit)
						if err != nil {
							panic(fmt.Errorf("error while getting repo list for search %q: %s", query, err))
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

					took := NewTimer()
					Infof("Getting list of followed projects...")
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

					toBeFollowed := make([]string, 0)
					// exclude already-followed projects:
					for _, repoURL := range repoURLs {
						_, isFollowed := isAlreadyFollowedProject(projects, repoURL)
						_, isFollowedProto := isAlreadyFollowedProto(protoProjects, repoURL)
						isNOTFollowed := !isFollowed && !isFollowedProto
						if isNOTFollowed {
							toBeFollowed = append(toBeFollowed, repoURL)
						}
					}

					toBeFollowed = Deduplicate(toBeFollowed)

					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile("follow-by-code-search", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// if the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								// sleep:
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)
					return nil
				},
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max number of code results.",
					},
					&cli.BoolFlag{
						Name:  "force, y",
						Usage: "Don't ask for confirmation.",
					},
				},
			},
			{
				Name:  "follow-and-add-to-list-by-code-search",
				Usage: "Follow projects and adds them to a list by custom search on repositories code.",
				Action: func(c *cli.Context) error {

					// rerquired
					query := c.Args().First()
					if query == "" {
						Fataln("must provide a query string")
					}
					projectListKeys := c.StringSlice("list-key")
					if len(projectListKeys) == 0 {
						Fataln("must provide a list key")
					}

					limit := c.Int("limit")
					force := c.Bool("y")
					noCheck := c.Bool("no-check")

					repoURLs := make([]string, 0)
					repoNames := make([]string, 0)
					{
						Debugf("Getting list of repos for search: %s ...", ShakespeareBG(query))
						repos, err := GithubListReposByCodeSearch(query, limit)
						if err != nil {
							panic(fmt.Errorf("error while getting repo list for search %q: %s", query, err))
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

							repoURLs = append(repoURLs, repo.GetHTMLURL())    // e.g. "https://github.com/kubernetes/dashboard"
							repoNames = append(repoNames, repo.GetFullName()) // e.g. "kubernetes/dashboard"
						}
					}

					toBeFollowed := make([]string, 0)
					if !noCheck { // exclude already-followed projects:
						took := NewTimer()
						Infof("Getting list of followed projects...")
						projects, protoProjects, err := client.ListFollowedProjects()
						if err != nil {
							panic(err)
						}
						Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

						for _, repoURL := range repoURLs {
							_, isFollowed := isAlreadyFollowedProject(projects, repoURL)
							_, isFollowedProto := isAlreadyFollowedProto(protoProjects, repoURL)
							isNOTFollowed := !isFollowed && !isFollowedProto
							if isNOTFollowed {
								toBeFollowed = append(toBeFollowed, repoURL)
							}
						}
						toBeFollowed = Deduplicate(toBeFollowed)
					} else {
						toBeFollowed = Deduplicate(repoURLs)
						repoNames = Deduplicate(repoNames)
					}

					totalToBeFollowed := len(toBeFollowed)
					Infof("Will follow %v projects...", totalToBeFollowed)
					if !force {
						CLIMustConfirmYes("Do you want to continue?")
					}

					// Write toBeFollowed to temp file:
					saveTargetListToTempFile("follow-by-code-search", toBeFollowed)

					followedNew := 0

					etac := eta.New(int64(totalToBeFollowed))

					// follow repos:
					for _, repoURL := range toBeFollowed {
						envelope := follower(repoURL, etac)
						if envelope != nil {
							// if the project was NOT already known to lgtm.com,
							// sleep to avoid triggering too many new builds:
							isNew := !envelope.IsKnown()
							if isNew {
								followedNew++
								// sleep:
								time.Sleep(waitDuration)
							}
						}
					}
					Successf("Followed %v projects (%v new)", totalToBeFollowed, followedNew)

					// add them to list
					totalRepoNames := len(repoNames)
					for len(repoNames) != 0 {
						for repoNameIndex, repoName := range repoNames {
							for _, projectListKey := range projectListKeys {
								success, err := client.AddProjectToSelectionFromRepoName(projectListKey, repoName)
								if err != nil {
									panic(err)
								}
								if !success {
									Warnf("Retrying later %s...", repoName) // Repo is new and hasn't been analysed yet.
									break
								} else {
									Successf("Added %s to %s.", repoName, projectListKey)
									repoNames = append(repoNames[:repoNameIndex], repoNames[repoNameIndex+1:]...) // https://www.delftstack.com/howto/go/how-to-delete-an-element-from-a-slice-in-golang/ (We need to preserve order)
								}
							}
						}
					}
					Successf("Added %v projects to provided projects' keys", totalRepoNames)

					return nil
				},
				Flags: []cli.Flag{
					&cli.IntFlag{
						Name:  "limit",
						Usage: "Max number of code results.",
					},
					&cli.BoolFlag{
						Name:  "force, y",
						Usage: "Don't ask for confirmation.",
					},
					&cli.BoolFlag{
						Name:  "no-check",
						Usage: "Don't check for followed projects.",
					},
					&cli.StringSliceFlag{
						Name:  "list-key, lk",
						Usage: "Project list key to add the repo (can specify multiple).",
					},
				},
			},
			{
				Name:  "query",
				Usage: "Run a query on one or multiple projects.",
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
						panic(Sf("file is not a .ql: %s", queryFilepath))
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
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
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
						Infof("Getting list of followed projects...")
						projects, protoProjects, err := client.ListFollowedProjects()
						if err != nil {
							panic(err)
						}

						IsProto := func(projectURL string) (*ProtoProject, bool) {
							for _, pr := range protoProjects {
								found := projectURL == pr.CloneURL
								if found {
									return pr, true
								}
							}
							return nil, false
						}

						excluded := c.StringSlice("exclude")

						// if no repos specified, and flag --all is true, then query all:
						if c.Bool("all") {
							Infof("Gonna query all %v projects", len(projects))
							for _, pr := range projects {
								repoURLs = append(repoURLs, pr.ExternalURL.URL)
							}
						}
						repoURLs = Deduplicate(repoURLs)

						for _, repoURL := range repoURLs {

							_, isProto := IsProto(repoURL)
							if isProto {
								Warnf("%s is proto; skipping", trimGithubPrefix(repoURL))
								continue
							}

							pr, already := isAlreadyFollowedProject(projects, repoURL)
							if !already {
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
					}

					projectListKeys := c.StringSlice("list-key")

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
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "exclude, e",
						Usage: "Exclude project; example: github/api",
					},
					&cli.StringSliceFlag{
						Name:  "list-key, lk",
						Usage: "Project list key on which to run the query (can specify multiple).",
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
			},
			{
				Name:  "rebuild-proto",
				Usage: "(Re)build followed proto-projects.",
				Action: func(c *cli.Context) error {

					took := NewTimer()
					Infof("Getting list of followed projects...")
					_, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("Currently %v proto-projects are followed; took %s", len(protoProjects), took())

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
			},
			{
				Name:  "rebuild",
				Usage: "Rebuild followed projects.",
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
					Infof("Currently %v projects (and %v proto) are followed; took %s", len(projects), len(protoProjects), took())

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

						// rebuild if a project does not support the specified language.
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
			},
			{
				Name:  "followed",
				Usage: "List all followed projects.",
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
				Flags: []cli.Flag{},
			},
			{
				Name:  "lists",
				Usage: "List all lists of projects.",
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
					for _, list := range lists {
						Sfln(
							"%s | %s",
							list.Name,
							list.Key,
						)
					}

					return nil
				},
				Flags: []cli.Flag{},
			},
			{
				Name:  "create-list",
				Usage: "Create a new list.",
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
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "Name of the list to be created.",
					},
				},
			},
			{
				Name:  "delete-list",
				Usage: "Delete a list.",
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
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "Name of the list to be created.",
					},
				},
			},
			{
				Name:  "list",
				Usage: "List projects inside a list by its name.",
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
					chunkSize := 100
					partsNumber := projectCount / chunkSize
					if projectCount < chunkSize {
						partsNumber = 1
					} else {
						partsNumber++
					}

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
				Flags: []cli.Flag{},
			},
			{
				Name:  "add-to-list",
				Usage: "Add followed projects to a list.",
				Action: func(c *cli.Context) error {

					repoURLsRaw := []string(c.Args())
					hasRepoListFilepath := c.IsSet("f")
					if hasRepoListFilepath {
						repoListFilepaths := c.StringSlice("f")
						for _, path := range repoListFilepaths {
							err := ReadConfigLinesAsString(path, func(line string) bool {
								repoURLsRaw = append(repoURLsRaw, line)
								return true
							})
							if err != nil {
								return err
							}
						}
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

					///
					Infof("Getting list of followed projects...")
					took := NewTimer()
					projects, protoProjects, err := client.ListFollowedProjects()
					if err != nil {
						panic(err)
					}
					Infof("took %s", took())

					name := c.String("name")
					took = NewTimer()
					Infof("Getting projects of %q list...", name)
					resp, err := client.ListProjectsInSelection(name)
					if err != nil {
						panic(err)
					}
					Infof("took %s", took())

					projectKeys := make([]string, 0)
					for _, repoURL := range repoURLs {
						project, isFollowed := isAlreadyFollowedProject(projects, repoURL)
						protoProject, isFollowedProto := isAlreadyFollowedProto(protoProjects, repoURL)
						if !isFollowed && !isFollowedProto {
							Warnf(
								"project %s is not followed",
								trimGithubPrefix(repoURL),
							)
						}

						if isFollowed && project != nil && !SliceContains(resp.ProjectKeys, project.Key) {
							projectKeys = append(projectKeys, project.Key)
						}
						if isFollowedProto && protoProject != nil && !SliceContains(resp.ProjectKeys, protoProject.Key) {
							// TODO: fix
							//projectKeys = append(projectKeys, protoProject.Key)
						}
					}

					Infof(
						"Adding %v projects to list %q...",
						len(projectKeys),
						name,
					)
					err = client.AddProjectToSelection(resp.Identity.Key, projectKeys...)
					if err != nil {
						panic(err)
					}
					return nil
				},
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "name",
						Usage: "Name of the list to which the projects will be added to.",
					},
					&cli.StringSliceFlag{
						Name:  "repos, f",
						Usage: "Filepath to text file with list of repos.",
					},
				},
			},
			{
				Name:  "x-list-query-results",
				Usage: "[x] List projects of a query run (json).",
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
					chunkSize := 100
					partsNumber := projectCount / chunkSize
					if projectCount < chunkSize {
						partsNumber = 1
					} else {
						partsNumber++
					}

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

func saveTargetListToTempFile(cmdName string, targets []string) {
	scanName := Sf(
		"lgtml-cli-%s-%s",
		cmdName,
		time.Now().Format(FilenameTimeFormat),
	)
	tmpfile, err := ioutil.TempFile("", scanName+".*.txt")
	if err != nil {
		log.Fatal(err)
	}

	writer := bufio.NewWriter(tmpfile)

	for _, target := range targets {
		_, err := writer.WriteString(target + "\n")
		if err != nil {
			tmpfile.Close()
			log.Fatal(err)
		}
	}

	if err := writer.Flush(); err != nil {
		log.Fatal(err)
	}

	fmt.Println(Sf(PurpleBG("Wrote compiled list of targets to temp file %s"), tmpfile.Name()))

	if err := tmpfile.Close(); err != nil {
		log.Fatal(err)
	}
}

// formatNotOKStatusCodeError is used to format an error when the status code is not 200.
func formatNotOKStatusCodeError(resp *request.Response) error {
	body, err := resp.Text()
	if err != nil {
		panic(err)
	}
	return fmt.Errorf(
		"Status code: %v\nHeader:\n%s\nBody:\n\n %s",
		resp.StatusCode,
		Sq(resp.Header),
		body,
	)
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
