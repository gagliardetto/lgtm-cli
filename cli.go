package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
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
				Usage:       "path to credentials.json file",
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
				Warnf("No config provided. Please specify the path to the config file with the LGTM_CLI_CONFIG env var.")
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
					Debugf("{%v/%v}", resp.Rate.Remaining, resp.Rate.Limit)
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
					hasRepoList := c.IsSet("f")
					if hasRepoList {
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
						isToBeUnfollowed := SliceContains(repoURLs, pr.ExternalURL.URL)
						if isToBeUnfollowed {
							toBeUnfollowed = append(toBeUnfollowed, pr)
						}
					}
					Infof("Will unfollow %v projects...", len(toBeUnfollowed))

					etac := eta.New(int64(len(repoURLs)))

					// unfollow projects:
					for _, pr := range toBeUnfollowed {
						unfollower(false, pr.Key, pr.ExternalURL.URL, etac)
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
					hasRepoList := c.IsSet("f")
					if hasRepoList {
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

					{ // write toBeFollowed to temp file:
						scanName := "lgtml-cli-follow-" + time.Now().Format(FilenameTimeFormat)
						tmpfile, err := ioutil.TempFile("", scanName+".*.txt")
						if err != nil {
							log.Fatal(err)
						}

						writer := bufio.NewWriter(tmpfile)

						for _, target := range toBeFollowed {
							_, err := writer.WriteString(target + "\n")
							if err != nil {
								tmpfile.Close()
								log.Fatal(err)
							}
						}

						fmt.Println(Sf(PurpleBG("wrote compiled toBeFollowed list to temp file %s"), tmpfile.Name()))

						if err := tmpfile.Close(); err != nil {
							log.Fatal(err)
						}
					}
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
					repoURLs := make([]string, 0)

					Debugf("Getting list of repos for language: %s ...", lang)
					repos, err := ListAllReposByLanguage(lang)
					if err != nil { //			Why is err undefined?
						panic(fmt.Errorf("error while getting repo list for language %q: %s", lang, err)) //
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

					{ // write toBeFollowed to temp file:
						scanName := "lgtml-cli-follow-" + time.Now().Format(FilenameTimeFormat)
						tmpfile, err := ioutil.TempFile("", scanName+".*.txt")
						if err != nil {
							log.Fatal(err)
						}

						writer := bufio.NewWriter(tmpfile)

						for _, target := range toBeFollowed {
							_, err := writer.WriteString(target + "\n")
							if err != nil {
								tmpfile.Close()
								log.Fatal(err)
							}
						}

						fmt.Println(Sf(PurpleBG("wrote compiled toBeFollowed list to temp file %s"), tmpfile.Name()))

						if err := tmpfile.Close(); err != nil {
							log.Fatal(err)
						}
					}
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
			},
			{
				Name:  "follow-by-search-meta",
				Usage: "Follow projects by custom search on repositories meta.",
				Action: func(c *cli.Context) error {

					query := c.Args().First()
					repoURLs := make([]string, 0)

					Debugf("Getting list of repos for search: %s ...", ShakespeareBG(query))
					repos, err := GithubListReposByMetaSearch(query)
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

					{ // write toBeFollowed to temp file:
						scanName := "lgtml-cli-follow-" + time.Now().Format(FilenameTimeFormat)
						tmpfile, err := ioutil.TempFile("", scanName+".*.txt")
						if err != nil {
							log.Fatal(err)
						}

						writer := bufio.NewWriter(tmpfile)

						for _, target := range toBeFollowed {
							_, err := writer.WriteString(target + "\n")
							if err != nil {
								tmpfile.Close()
								log.Fatal(err)
							}
						}

						fmt.Println(Sf(PurpleBG("wrote compiled toBeFollowed list to temp file %s"), tmpfile.Name()))

						if err := tmpfile.Close(); err != nil {
							log.Fatal(err)
						}
					}
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
			},
			{
				Name:  "follow-by-code-search",
				Usage: "Follow projects by custom search on repositories code.",
				Action: func(c *cli.Context) error {

					query := c.Args().First()
					repoURLs := make([]string, 0)

					Debugf("Getting list of repos for search: %s ...", ShakespeareBG(query))
					repos, err := GithubListReposByCodeSearch(query)
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

					{ // write toBeFollowed to temp file:
						scanName := "lgtml-cli-follow-" + time.Now().Format(FilenameTimeFormat)
						tmpfile, err := ioutil.TempFile("", scanName+".*.txt")
						if err != nil {
							log.Fatal(err)
						}

						writer := bufio.NewWriter(tmpfile)

						for _, target := range toBeFollowed {
							_, err := writer.WriteString(target + "\n")
							if err != nil {
								tmpfile.Close()
								log.Fatal(err)
							}
						}

						fmt.Println(Sf(PurpleBG("wrote compiled toBeFollowed list to temp file %s"), tmpfile.Name()))

						if err := tmpfile.Close(); err != nil {
							log.Fatal(err)
						}
					}
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
					hasRepoList := c.IsSet("f")
					if hasRepoList {
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
					hasRepoList := c.IsSet("f")
					if hasRepoList {
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
func ListAllReposByLanguage(lang string) ([]*github.Repository, error) {
	lang = strings.TrimSpace(lang)

	opts := &ghc.ListAllReposByLanguageOpts{
		Language:     lang,
		ExcludeForks: true,
		// Limit:        limit, // Maybe add a `--limit=n` flag?
	}
	repos, err := ghClient.ListAllReposByLanguage(opts)
	if err != nil {
		return nil, err
	}

	return repos, nil
}
func GithubListReposByMetaSearch(query string) ([]*github.Repository, error) {
	return ghClient.SearchRepos(query)
}
func GithubListReposByCodeSearch(query string) ([]*github.Repository, error) {
	return ghClient.SearchCode(query)
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

type Client struct {
	conf *Config
}

func NewClient(conf *Config) (*Client, error) {
	if conf == nil {
		return nil, errors.New("conf is nil")
	}
	if err := conf.Validate(); err != nil {
		return nil, err
	}

	cl := &Client{
		conf: conf,
	}
	return cl, nil
}

func (cl *Client) ListFollowedProjects() ([]*Project, []*ProtoProject, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, nil, err
	}

	resp, err := req.Get("https://lgtm.com/internal_api/v0.2/getMyProjects?apiVersion=" + cl.conf.APIVersion)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response ProjectListResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, nil, fmt.Errorf("error while unmarshaling: %s", err)
	}
	projectList := make([]*Project, 0)
	protoProjectList := make([]*ProtoProject, 0)
	for _, envelope := range response.Data {
		prj := envelope.MustGetProject()
		if prj != nil {
			projectList = append(projectList, prj)
		}

		protoPrj := envelope.MustGetProtoProject()
		if protoPrj != nil {
			protoProjectList = append(protoProjectList, protoPrj)
		}
	}

	return projectList, protoProjectList, nil
}

type CommonResponse struct {
	Status string `json:"status"`
}

const STATUS_SUCCESS_STRING = "success"

func (cl *Client) UnfollowProject(key string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"project_key": key,
		"apiVersion":  cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/unfollowProject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}
func (cl *Client) UnfollowProtoProject(key string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"protoproject_key": key,
		"apiVersion":       cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/unfollowProtoproject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

type FollowProjectResponse struct {
	Status string    `json:"status"`
	Data   *Envelope `json:"data"`
}

func (cl *Client) FollowProject(u string) (*Envelope, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}
	req.Data = map[string]string{
		"url":        u,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/followProject")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response FollowProjectResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

func (cl *Client) DeleteProjectSelection(name string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"name":       name,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/deleteProjectSelection")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

func (cl *Client) CreateProjectSelection(name string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"name":       name,
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/createProjectSelection")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}
func formatStringArray(sl ...string) string {
	if len(sl) == 0 {
		return "[]"
	}
	marshaled, err := json.Marshal(sl)
	if err != nil {
		panic(err)
	}
	return string(marshaled)
}
func (cl *Client) AddProjectToSelection(selectionID string, projectKeys ...string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"projectSelectionId": selectionID,
		"addedProjects":      formatStringArray(projectKeys...),
		"removedProjects":    "[]",
		"apiVersion":         cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/updateProjectSelection")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}
	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

type SearchSuggestionsResponse struct {
	Status string                  `json:"status"`
	Data   []*SearchSuggestionItem `json:"data"`
}
type SearchSuggestionItem struct {
	Text       string `json:"text"`
	URL        string `json:"url"`
	ProjectKey string `json:"projectKey"`
}

func (cl *Client) GetSearchSuggestions(str string) ([]*SearchSuggestionItem, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getSearchSuggestions?searchSuggestions=%s&apiVersion=%s",
			str,
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response SearchSuggestionsResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	return response.Data, nil
}

type ProjectSelectionListResponse struct {
	Status string                  `json:"status"`
	Data   []*ProjectSelectionBare `json:"data"`
}
type ProjectSelectionBare struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

func (cl *Client) ListProjectSelections() ([]*ProjectSelectionBare, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}
	req.Data = map[string]string{
		"apiVersion": cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/getUsedProjectSelections")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response ProjectSelectionListResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type ListProjectsInSelectionResponse struct {
	Status string                `json:"status"`
	Data   *ProjectSelectionFull `json:"data"`
}
type Identity struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}
type ProjectSelectionFull struct {
	Identity    Identity `json:"identity"`
	ProjectKeys []string `json:"projectKeys"`
}

func (cl *Client) ListProjectsInSelection(name string) (*ProjectSelectionFull, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getProjectSelectionByName?name=%s&apiVersion=%s",
			name,
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response ListProjectsInSelectionResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type QueryConfig struct {
	Lang                 string
	ProjectKeys          []string
	ProjectSelectionKeys []string
	QueryString          string
}

type QueryResponse struct {
	Status string            `json:"status"`
	Data   QueryResponseData `json:"data"`
}
type Stats struct {
	AllRuns                int `json:"all_runs"`
	Failed                 int `json:"failed"`
	FinishedWithResults    int `json:"finished_with_results"`
	FinishedWithoutResults int `json:"finished_without_results"`
	Incomplete             int `json:"incomplete"`
	PendingSchedulingTasks int `json:"pendingSchedulingTasks"`
}
type QueryResponseData struct {
	Key                  string        `json:"key"`
	QueryText            string        `json:"queryText"`
	ExecutionDate        int64         `json:"executionDate"`
	LanguageKey          string        `json:"languageKey"`
	ProjectKeys          []string      `json:"projectKeys"`
	ProjectSelectionKeys []interface{} `json:"projectSelectionKeys"`
	QueryAllProjects     bool          `json:"queryAllProjects"`
	Stats                Stats         `json:"stats"`
}

//
func (qrd *QueryResponseData) GetResultLink() string {
	return Sf("https://lgtm.com/query/%s/", qrd.Key)
}

func (cl *Client) Query(conf *QueryConfig) (*QueryResponseData, error) {

	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}
	req.Data = map[string]string{
		"lang":                 conf.Lang,
		"projectKeys":          formatStringArray(conf.ProjectKeys...),
		"projectSelectionKeys": formatStringArray(conf.ProjectSelectionKeys...),
		"queryString":          conf.QueryString,
		"queryAllProjects":     "false",
		"guessedLocation":      "",
		"apiVersion":           cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/runQuery")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response QueryResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return &response.Data, nil
}

var (
	DefaultMaxIdleConnsPerHost = 50
	Timeout                    = 5 * time.Minute
	DefaultKeepAlive           = 180 * time.Second
)

var (
	httpClient = NewHTTP()
)

func NewHTTPTransport() *http.Transport {
	return &http.Transport{
		IdleConnTimeout:     Timeout,
		MaxIdleConnsPerHost: DefaultMaxIdleConnsPerHost,
		Proxy:               http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   Timeout,
			KeepAlive: DefaultKeepAlive,
		}).Dial,
		//TLSClientConfig: &tls.Config{
		//	InsecureSkipVerify: conf.InsecureSkipVerify,
		//},
	}
}

// NewHTTP returns a new Client from the provided config.
// Client is safe for concurrent use by multiple goroutines.
func NewHTTP() *http.Client {
	tr := NewHTTPTransport()

	return &http.Client{
		Timeout:   Timeout,
		Transport: tr,
	}
}

func (cl *Client) newRequest() (*request.Request, error) {
	apiRateLimiter.Take()

	req := request.NewRequest(httpClient)
	req.Headers = map[string]string{
		"authority":        "lgtm.com",
		"accept":           "*/*",
		"lgtm-nonce":       cl.conf.Session.Nonce,
		"dnt":              "1",
		"x-requested-with": "XMLHttpRequest",
		"user-agent":       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_14_0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/73.0.3683.103 Safari/537.36",
		"sec-fetch-site":   "same-origin",
		"sec-fetch-mode":   "cors",
		"referer":          "https://lgtm.com/dashboard",
		"accept-encoding":  "gzip",
	}

	req.Cookies = map[string]string{
		"lgtm_long_session":  cl.conf.Session.LongSession,
		"lgtm_short_session": cl.conf.Session.ShortSession,
		"_consent_settings":  "accepted",
	}

	return req, nil
}

type LGTMSession struct {
	Nonce        string `json:"nonce"`
	ShortSession string `json:"short_session"`
	LongSession  string `json:"long_session"`
}

// Validate validates
func (sess *LGTMSession) Validate() error {
	if sess.Nonce == "" {
		return errors.New("sess.Nonce is not set")
	}
	if sess.ShortSession == "" {
		return errors.New("sess.ShortSession is not set")
	}
	if sess.LongSession == "" {
		return errors.New("sess.LongSession is not set")
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
		return errors.New("conf.APIVersion not provided")
	}

	if conf.Session == nil {
		return errors.New("conf.Session is nil")
	}

	return conf.Session.Validate()
}

type Envelope struct {
	RawRealProject     interface{} `json:"realProject"`
	RawProtoProject    interface{} `json:"protoproject"`
	parsedproject      *Project
	parsedProtoProject *ProtoProject
}

//
func (env *Envelope) MustGetProject() *Project {
	if env.parsedproject != nil {
		return env.parsedproject
	}
	if env.RawRealProject == nil {
		return nil
	}

	var slice []interface{}
	err := TranscodeJSON(env.RawRealProject, &slice)
	if err != nil {
		panic(err)
	}
	firstObjectInterface := slice[0]

	var parsedproject Project
	err = TranscodeJSON(firstObjectInterface, &parsedproject)
	if err != nil {
		panic(err)
	}
	env.parsedproject = &parsedproject
	return env.parsedproject
}

// IsKnown returns whether the projects was already known to lgtm.com
func (env *Envelope) IsKnown() bool {
	isFirstBuild := env.MustGetProject() == nil && env.MustGetProtoProject() != nil
	return !isFirstBuild
}

func (env *Envelope) MustGetProtoProject() *ProtoProject {
	if env.parsedProtoProject != nil {
		return env.parsedProtoProject
	}
	if env.RawProtoProject == nil {
		return nil
	}

	var proto ProtoProject
	err := TranscodeJSON(env.RawProtoProject, &proto)
	if err != nil {
		panic(err)
	}
	env.parsedProtoProject = &proto

	return env.parsedProtoProject
}

type ProtoProject struct {
	Key              string `json:"key"`
	DisplayName      string `json:"displayName"`
	State            string `json:"state"`
	BuildAttemptKey  string `json:"buildAttemptKey"`
	NextBuildStarted bool   `json:"nextBuildStarted"`
	CloneURL         string `json:"cloneUrl"`
}

type Project struct {
	Key                string               `json:"key"`
	Languages          []string             `json:"languages"`
	TotalLanguageChurn []TotalLanguageChurn `json:"totalLanguageChurn"`
	RepoProvider       string               `json:"repoProvider"`
	DisplayName        string               `json:"displayName"`
	Slug               string               `json:"slug"`
	ExternalURL        ExternalURL          `json:"externalURL"`
	AdminURL           string               `json:"adminURL"`
	Modes              Modes                `json:"modes"`
}

func (pr *Project) SupportsLanguage(lang string) bool {
	return SliceContains(pr.Languages, lang)
}

type TotalLanguageChurn struct {
	Lang  string `json:"lang"`
	Churn int    `json:"churn"`
}
type ExternalURL struct {
	URL   string `json:"url"`
	Name  string `json:"name"`
	Theme string `json:"theme"`
}
type Modes map[string]string

type ProjectListResponse struct {
	Status string      `json:"status"`
	Data   []*Envelope `json:"data"`
}

// Info should be used to describe the example commands that are about to run.
func Info(format string, args ...interface{}) {
	fmt.Printf("\x1b[34;1m%s\x1b[0m\n", fmt.Sprintf(format, args...))
}

// Warning should be used to display a warning
func Warning(format string, args ...interface{}) {
	fmt.Printf("\x1b[36;1m%s\x1b[0m\n", fmt.Sprintf(format, args...))
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

func (cl *Client) RebuildProtoProject(key string) error {

	req, err := cl.newRequest()
	if err != nil {
		return err
	}
	req.Data = map[string]string{
		"config":           "",
		"protoproject_key": key,
		"apiVersion":       cl.conf.APIVersion,
	}

	resp, err := req.Post("https://lgtm.com/internal_api/v0.2/rebuildProtoproject")
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}

	return nil
}

const (
	LangGo         = "go"
	LangCPP        = "cpp"
	LangCSharp     = "csharp"
	LangJava       = "java"
	LangJavaScript = "javascript"
	LangPython     = "python"
)

// NewBuildAttempt allows to attempt a build for a language NOT previously built.
func (cl *Client) NewBuildAttempt(projectKey string, lang string) error {
	req, err := cl.newRequest()
	if err != nil {
		return err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/newBuildAttempt?projectKey=%s&language=%s&apiVersion=%s",
			projectKey,
			lang,
			cl.conf.APIVersion,
		))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}
	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}
	return nil
}

// RequestTestBuild triggers re-build for the specified language(s).
func (cl *Client) RequestTestBuild(urlIdentifier string, langs ...string) error {
	req, err := cl.newRequest()
	if err != nil {
		return err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/"+
				"urlIdentifier=%s&languages=%s&config=&apiVersion=%s",
			urlIdentifier,
			url.QueryEscape(formatStringArray(langs...)),
			cl.conf.APIVersion,
		))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response CommonResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return fmt.Errorf("error while unmarshaling: %s", err)
	}
	if response.Status != STATUS_SUCCESS_STRING {
		return fmt.Errorf("status string is not success: %s", response.Status)
	}
	return nil
}

type GetProjectLatestStateStatsResponse struct {
	Status string                `json:"status"`
	Data   *LatestStateStatsData `json:"data"`
}
type Rating struct {
	Score    float64 `json:"score"`
	Grade    string  `json:"grade"`
	RawGrade float64 `json:"rawGrade"`
}
type RevisionName struct {
	Value       string `json:"value"`
	PrettyValue string `json:"prettyValue"`
}
type SecurityAwareness struct {
	Score             float64 `json:"score"`
	NumSecurityAlerts int     `json:"numSecurityAlerts"`
	Grade             string  `json:"grade"`
	Percentile        float64 `json:"percentile"`
}
type LanguageStates struct {
	Lang              string            `json:"lang"`
	SnapshotDate      int64             `json:"snapshotDate"`
	TotalAlerts       int               `json:"totalAlerts"`
	TotalLines        int               `json:"totalLines"`
	Rating            Rating            `json:"rating,omitempty"`
	RevisionName      RevisionName      `json:"revisionName"`
	SecurityAwareness SecurityAwareness `json:"securityAwareness,omitempty"`
}
type LatestStateStatsData struct {
	NumContributors int              `json:"numContributors"`
	LanguageStates  []LanguageStates `json:"languageStates"`
}

func (cl *Client) GetProjectLatestStateStats(projectKey string) (*LatestStateStatsData, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getProjectLatestStateStats?key=%s&apiVersion=%s",
			projectKey,
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response GetProjectLatestStateStatsResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
}

type GetProjectsByKeyResponse struct {
	Status string                        `json:"status"`
	Data   *GetProjectsByKeyResponseData `json:"data"`
}

type GetProjectsByKeyResponseData struct {
	FullProjects map[string]*Project    `json:"fullProjects"`
	AnonProjects map[string]interface{} `json:"anonProjects"`
}

func (data *GetProjectsByKeyResponseData) GetProject(key string) *Project {
	val, ok := data.FullProjects[key]
	if ok {
		return val
	}
	return nil
}

func (cl *Client) GetProjectsByKey(keys ...string) (*GetProjectsByKeyResponseData, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	resp, err := req.Get(
		Sf(
			"https://lgtm.com/internal_api/v0.2/getProjectsByKey?keys=%s&apiVersion=%s",
			formatStringArray(keys...),
			cl.conf.APIVersion,
		),
	)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := resp.Text()
		if err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("status code is %v; body:\n\n %s", resp.StatusCode, body)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response GetProjectsByKeyResponse
	err = func() error {
		defer closer()
		defer resp.Body.Close()
		decoder := json.NewDecoder(reader)

		return decoder.Decode(&response)
	}()
	if err != nil {
		return nil, fmt.Errorf("error while unmarshaling: %s", err)
	}

	if response.Status != STATUS_SUCCESS_STRING {
		return nil, fmt.Errorf("status string is not success: %s", response.Status)
	}

	return response.Data, nil
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
	withDotGitSuffix := ""
	if !strings.HasSuffix(projectURL, ".git") {
		withDotGitSuffix = projectURL + ".git"
	} else {
		withDotGitSuffix = projectURL
	}
	for _, pr := range protoProjects {
		alreadyFollowed := (ToLower(projectURL) == ToLower(pr.CloneURL)) || (ToLower(withDotGitSuffix) == ToLower(pr.CloneURL))
		if alreadyFollowed {
			return pr, true
		}
	}
	return nil, false
}

func trimGithubPrefix(s string) string {
	return strings.TrimPrefix(s, "https://github.com/")
}
