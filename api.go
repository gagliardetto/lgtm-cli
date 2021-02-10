package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gagliardetto/request"
	. "github.com/gagliardetto/utilz"
)

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
		return nil, nil, formatHTTPNotOKStatusCodeError(resp)
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

const (
	STATUS_SUCCESS_STRING = "success"
	STATUS_ERROR_STRING   = "error"
)

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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
	}

	return nil
}

type FollowProjectResponse struct {
	*StatusResponse
	Data *Envelope `json:"data"`
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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
		return nil, response.StatusResponse
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
	}

	return nil
}

type SearchSuggestionsResponse struct {
	*StatusResponse
	Data []*SearchSuggestionItem `json:"data"`
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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
	if response.Status != STATUS_SUCCESS_STRING {
		return nil, response.StatusResponse
	}

	return response.Data, nil
}

type ProjectSelectionListResponse struct {
	*StatusResponse
	Data []*ProjectSelectionBare `json:"data"`
}
type ProjectSelectionBare struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type ProjectSelectionBareSlice []*ProjectSelectionBare

//
func (lists ProjectSelectionBareSlice) ByName(name string) *ProjectSelectionBare {
	for _, v := range lists {
		if v.Name == name {
			return v
		}
	}
	return nil
}

func (cl *Client) ListProjectSelections() (ProjectSelectionBareSlice, error) {

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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
		return nil, response.StatusResponse
	}

	return response.Data, nil
}

type ListProjectsInSelectionResponse struct {
	*StatusResponse
	Data *ProjectSelectionFull `json:"data"`
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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
		return nil, response.StatusResponse
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
	*StatusResponse
	Data QueryResponseData `json:"data"`
}
type QueryResponseStats struct {
	AllRuns                int `json:"all_runs"`
	Failed                 int `json:"failed"`
	FinishedWithResults    int `json:"finished_with_results"`
	FinishedWithoutResults int `json:"finished_without_results"`
	Incomplete             int `json:"incomplete"`
	PendingSchedulingTasks int `json:"pendingSchedulingTasks"`
}
type QueryResponseData struct {
	Key                  string             `json:"key"`
	QueryText            string             `json:"queryText"`
	ExecutionDate        int64              `json:"executionDate"`
	LanguageKey          string             `json:"languageKey"`
	ProjectKeys          []string           `json:"projectKeys"`
	ProjectSelectionKeys []string           `json:"projectSelectionKeys"`
	QueryAllProjects     bool               `json:"queryAllProjects"`
	Stats                QueryResponseStats `json:"stats"`
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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
		return nil, response.StatusResponse
	}

	return &response.Data, nil
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
	*StatusResponse
	Data []*Envelope `json:"data"`
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
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
		return formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return fmt.Errorf("error while getting Reader: %s", err)
	}
	var response StatusResponse
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
		return &response
	}
	return nil
}

type GetProjectLatestStateStatsResponse struct {
	*StatusResponse
	Data *LatestStateStatsData `json:"data"`
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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
		return nil, response.StatusResponse
	}

	return response.Data, nil
}

type GetProjectsByKeyResponse struct {
	*StatusResponse
	Data *GetProjectsByKeyResponseData `json:"data"`
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
		return nil, formatHTTPNotOKStatusCodeError(resp)
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
		return nil, response.StatusResponse
	}

	return response.Data, nil
}

type OrderBy string

const (
	OrderByNumAlerts    OrderBy = "num_alerts"
	OrderByNumResults   OrderBy = "num_results"
	OrderByRunTime      OrderBy = "run_time"
	OrderByProjectSize  OrderBy = "loc"
	OrderByAlertDensity OrderBy = "alert_density"
)

func (cl *Client) GetQueryResults(queryID string, orderBy OrderBy, startCursor string) (*GetQueryResultsResponseData, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, err
	}

	base := "https://lgtm.com/internal_api/v0.2/getQueryResults"
	vals := url.Values{}
	{
		vals.Set("queryId", queryID)
		vals.Set("limit", "10")
		vals.Set("orderBy", string(orderBy))
		if startCursor != "" {
			vals.Set("startCursor", startCursor)
		}
		vals.Set("apiVersion", cl.conf.APIVersion)
	}

	dst := base + "?" + vals.Encode()
	resp, err := req.Get(dst)
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
	var response GetQueryResultsResponse
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
		return nil, response.StatusResponse
	}

	return response.Data, nil
}

type GetQueryResultsResponse struct {
	*StatusResponse
	Data *GetQueryResultsResponseData `json:"data"`
}
type SrcVersion struct {
	Value       string `json:"value"`
	PrettyValue string `json:"prettyValue"`
}
type GetQueryResultsResponseStats struct {
	QueryRunKey          string   `json:"queryRunKey"`
	NumResults           int      `json:"numResults"`
	NumExcludedResults   int      `json:"numExcludedResults"`
	ResultsWereTruncated bool     `json:"resultsWereTruncated"`
	Columns              []string `json:"columns"`
	IsInAlertFormat      bool     `json:"isInAlertFormat"`
	HasAlertResults      bool     `json:"hasAlertResults"`
	NumAlerts            int      `json:"numAlerts"`
}
type GetQueryResultsResponseItem struct {
	Key         string                        `json:"key"`
	ProjectKey  string                        `json:"projectKey"`
	Lang        string                        `json:"lang"`
	SnapshotKey string                        `json:"snapshotKey"`
	SrcVersion  *SrcVersion                   `json:"srcVersion"`
	Done        bool                          `json:"done"`
	Stats       *GetQueryResultsResponseStats `json:"stats,omitempty"`
	Error       string                        `json:"error,omitempty"`
}
type GetQueryResultsResponseData struct {
	Cursor string                         `json:"cursor"`
	Items  []*GetQueryResultsResponseItem `json:"items"`
}
type GetProjectBySlugResponse struct {
	*StatusResponse
	Data *GetProjectBySlugResponseData `json:"data"`
}
type GetProjectBySlugResponseData struct {
	Left  *Project `json:"left"`
	Right *Right   `json:"right"`
}

type Right struct {
	RequestedURLIdentifier string   `json:"requestedUrlIdentifier"`
	Redirect               *Project `json:"redirect"`
}

type StatusResponse struct {
	Status      string `json:"status"`
	ErrorString string `json:"error"`
	Message     string `json:"message"`
}

//
func (status *StatusResponse) IsNotFound() bool {
	return status.Status == STATUS_ERROR_STRING && status.ErrorString == "not found"
}

func isStatusResponseError(err error) bool {
	_, ok := err.(*StatusResponse)
	return ok
}

//
func (status *StatusResponse) Error() string {
	if status.Status == STATUS_SUCCESS_STRING {
		return Sf(
			"resp.status=%q; resp.error=%q; resp.message=%q",
			status.Status,
			status.ErrorString,
			status.Message,
		)
	}
	return Sf(
		"resp.status is not 'success', but %q; resp.error=%q; resp.message=%q",
		status.Status,
		status.ErrorString,
		status.Message,
	)
}

func (cl *Client) GetProjectBySlug(slug string) (*Project, error) {
	req, err := cl.newRequest()
	if err != nil {
		return nil, fmt.Errorf("error while cl.newRequest: %s", err)
	}

	base := "https://lgtm.com/internal_api/v0.2/getProjectBySlug"
	vals := url.Values{}
	{
		vals.Set("slug", slug)
		vals.Set("apiVersion", cl.conf.APIVersion)
	}

	dst := base + "?" + vals.Encode()
	resp, err := req.Get(dst)
	if err != nil {
		return nil, fmt.Errorf("error while req.Get: %s", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, formatHTTPNotOKStatusCodeError(resp)
	}

	reader, closer, err := resp.DecompressedReaderFromPool()
	if err != nil {
		return nil, fmt.Errorf("error while getting Reader: %s", err)
	}
	var response GetProjectBySlugResponse
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
		return nil, response.StatusResponse
	}

	if response.Data == nil || (response.Data.Left == nil && response.Data.Right == nil) {
		return nil, formatRawResponseBody(resp)
	}

	if response.Data.Left != nil {
		return response.Data.Left, nil
	}

	return response.Data.Right.Redirect, nil
}

// formatHTTPNotOKStatusCodeError is used to format an error when the status code is not 200.
func formatHTTPNotOKStatusCodeError(resp *request.Response) error {
	{ // Try parsing the response body as a StatusResponse:
		reader, closer, err := resp.DecompressedReaderFromPool()
		if err != nil {
			panic(fmt.Errorf("error while getting Reader: %s", err))
		}
		var errResponse StatusResponse
		err = func() error {
			defer closer()
			defer resp.Body.Close()
			decoder := json.NewDecoder(reader)

			return decoder.Decode(&errResponse)
		}()
		if err == nil {
			return addRequestInfoToError(resp, &errResponse)
		}
	}

	return addRequestInfoToError(resp, formatRawResponseBody(resp))
}

func addRequestInfoToError(resp *request.Response, err error) error {
	if resp == nil || resp.Request == nil {
		return err
	}
	if resp.Request.Body != nil {
		reqBody, err := ioutil.ReadAll(resp.Request.Body)
		if err == nil {
			return fmt.Errorf(
				"%s\nRequest: %s %s (with %v content)\nBody:\n%s",
				err.Error(),
				resp.Request.Method,
				resp.Request.URL.String(),
				resp.Request.ContentLength,
				string(reqBody),
			)
		}
	}

	return fmt.Errorf(
		"%s\nRequest: %s %s",
		err.Error(),
		resp.Request.Method,
		resp.Request.URL.String(),
	)
}

func formatRawResponseBody(resp *request.Response) error {
	// Get the body as text:
	body, err := resp.Text()
	if err != nil {
		return fmt.Errorf("error while resp.Text: %s", err)
	}
	return fmt.Errorf(
		"Status code: %v\nHeader:\n%s\nBody:\n\n %s",
		resp.StatusCode,
		Sq(resp.Header),
		body,
	)
}
