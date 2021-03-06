package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sync"

	"github.com/pmylund/go-cache"
	"golang.org/x/crypto/nacl/box"
)

const bPathAnnounceStartup = "/startup"
const bPathAddTopic = "/topics"
const bPathAggStats = "/stats"
const bPathOtherCommand = "/cmd/"

type backendInfo struct {
	HTTPClient    http.Client
	baseURL       string
	responseCache *cache.Cache

	postStatisticsURL  string
	addTopicURL        string
	announceStartupURL string

	sharedKey [32]byte
	serverID  int

	lastSuccess     map[string]time.Time
	lastSuccessLock sync.Mutex
}

var Backend *backendInfo

func setupBackend(config *ConfigFile) *backendInfo {
	b := new(backendInfo)
	Backend = b
	b.serverID = config.ServerID

	b.HTTPClient.Timeout = 60 * time.Second
	b.baseURL = config.BackendURL
	b.responseCache = cache.New(60*time.Second, 120*time.Second)

	b.announceStartupURL = fmt.Sprintf("%s%s", b.baseURL, bPathAnnounceStartup)
	b.addTopicURL = fmt.Sprintf("%s%s", b.baseURL, bPathAddTopic)
	b.postStatisticsURL = fmt.Sprintf("%s%s", b.baseURL, bPathAggStats)

	epochTime := time.Unix(0, 0).UTC()
	lastBackendSuccess := map[string]time.Time{
		bPathAnnounceStartup: epochTime,
		bPathAddTopic:        epochTime,
		bPathAggStats:        epochTime,
		bPathOtherCommand:    epochTime,
	}
	b.lastSuccess = lastBackendSuccess

	var theirPublic, ourPrivate [32]byte
	copy(theirPublic[:], config.BackendPublicKey)
	copy(ourPrivate[:], config.OurPrivateKey)

	box.Precompute(&b.sharedKey, &theirPublic, &ourPrivate)

	return b
}

func getCacheKey(remoteCommand, data string) string {
	return fmt.Sprintf("%s/%s", remoteCommand, data)
}

// ErrForwardedFromBackend is an error returned by the backend server.
type ErrForwardedFromBackend struct {
	JSONError interface{}
}

func (bfe ErrForwardedFromBackend) Error() string {
	bytes, _ := json.Marshal(bfe.JSONError)
	return string(bytes)
}

// ErrAuthorizationNeeded is emitted when the backend replies with HTTP 401.
// Indicates that an attempt to validate `ClientInfo.TwitchUsername` should be attempted.
var ErrAuthorizationNeeded = errors.New("Must authenticate Twitch username to use this command")

// SendRemoteCommandCached performs a RPC call on the backend, but caches responses.
func (backend *backendInfo) SendRemoteCommandCached(remoteCommand, data string, auth AuthInfo) (string, error) {
	cached, ok := backend.responseCache.Get(getCacheKey(remoteCommand, data))
	if ok {
		return cached.(string), nil
	}
	return backend.SendRemoteCommand(remoteCommand, data, auth)
}

// SendRemoteCommand performs a RPC call on the backend by POSTing to `/cmd/$remoteCommand`.
// The form data is as follows: `clientData` is the JSON in the `data` parameter
// (should be retrieved from ClientMessage.Arguments), and either `username` or
// `usernameClaimed` depending on whether AuthInfo.UsernameValidates is true is AuthInfo.TwitchUsername.
func (backend *backendInfo) SendRemoteCommand(remoteCommand, data string, auth AuthInfo) (responseStr string, err error) {
	destURL := fmt.Sprintf("%s/cmd/%s", backend.baseURL, remoteCommand)
	healthBucket := fmt.Sprintf("/cmd/%s", remoteCommand)

	formData := url.Values{
		"clientData": []string{data},
		"username":   []string{auth.TwitchUsername},
	}

	if auth.UsernameValidated {
		formData.Set("authenticated", "1")
	} else {
		formData.Set("authenticated", "0")
	}

	sealedForm, err := backend.SealRequest(formData)
	if err != nil {
		return "", err
	}

	resp, err := backend.HTTPClient.PostForm(destURL, sealedForm)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	responseStr = string(respBytes)

	if resp.StatusCode == 401 {
		return "", ErrAuthorizationNeeded
	} else if resp.StatusCode != 200 {
		if resp.Header.Get("Content-Type") == "application/json" {
			var err2 ErrForwardedFromBackend
			err := json.Unmarshal(respBytes, &err2.JSONError)
			if err != nil {
				return "", fmt.Errorf("error decoding json error from backend: %v | %s", err, responseStr)
			}
			return "", err2
		}
		return "", httpError(resp.StatusCode)
	}

	if resp.Header.Get("FFZ-Cache") != "" {
		durSecs, err := strconv.ParseInt(resp.Header.Get("FFZ-Cache"), 10, 64)
		if err != nil {
			return "", fmt.Errorf("The RPC server returned a non-integer cache duration: %v", err)
		}
		duration := time.Duration(durSecs) * time.Second
		backend.responseCache.Set(getCacheKey(remoteCommand, data), responseStr, duration)
	}

	now := time.Now().UTC()
	backend.lastSuccessLock.Lock()
	defer backend.lastSuccessLock.Unlock()
	backend.lastSuccess[bPathOtherCommand] = now
	backend.lastSuccess[healthBucket] = now

	return
}

// SendAggregatedData sends aggregated emote usage and following data to the backend server.
func (backend *backendInfo) SendAggregatedData(form url.Values) error {
	sealedForm, err := backend.SealRequest(form)
	if err != nil {
		return err
	}

	resp, err := backend.HTTPClient.PostForm(backend.postStatisticsURL, sealedForm)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return httpError(resp.StatusCode)
	}

	backend.lastSuccessLock.Lock()
	defer backend.lastSuccessLock.Unlock()
	backend.lastSuccess[bPathAggStats] = time.Now().UTC()

	return resp.Body.Close()
}

// ErrBackendNotOK indicates that the backend replied with something other than the string "ok".
type ErrBackendNotOK struct {
	Response string
	Code     int
}

// Error Implements the error interface.
func (noe ErrBackendNotOK) Error() string {
	return fmt.Sprintf("backend returned %d: %s", noe.Code, noe.Response)
}

// SendNewTopicNotice notifies the backend that a client has performed the first subscription to a pub/sub topic.
// POST data:
// channels=room.trihex
// added=t
func (backend *backendInfo) SendNewTopicNotice(topic string) error {
	return backend.sendTopicNotice(topic, true)
}

// SendCleanupTopicsNotice notifies the backend that pub/sub topics have no subscribers anymore.
// POST data:
// channels=room.sirstendec,room.bobross,feature.foo
// added=f
func (backend *backendInfo) SendCleanupTopicsNotice(topics []string) error {
	return backend.sendTopicNotice(strings.Join(topics, ","), false)
}

func (backend *backendInfo) sendTopicNotice(topic string, added bool) error {
	formData := url.Values{}
	formData.Set("channels", topic)
	if added {
		formData.Set("added", "t")
	} else {
		formData.Set("added", "f")
	}

	sealedForm, err := backend.SealRequest(formData)
	if err != nil {
		return err
	}

	resp, err := backend.HTTPClient.PostForm(backend.addTopicURL, sealedForm)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		respBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return ErrBackendNotOK{Code: resp.StatusCode, Response: fmt.Sprintf("(error reading non-2xx response): %s", err.Error())}
		}
		return ErrBackendNotOK{Code: resp.StatusCode, Response: string(respBytes)}
	}

	backend.lastSuccessLock.Lock()
	defer backend.lastSuccessLock.Unlock()
	backend.lastSuccess[bPathAddTopic] = time.Now().UTC()

	return nil
}

func httpError(statusCode int) error {
	return fmt.Errorf("backend http error: %d", statusCode)
}

// GenerateKeys generates a new NaCl keypair for the server and writes out the default configuration file.
func GenerateKeys(outputFile, serverID, theirPublicStr string) {
	var err error
	output := ConfigFile{
		ListenAddr:      "0.0.0.0:8001",
		SSLListenAddr:   "0.0.0.0:443",
		BackendURL:      "http://localhost:8002/ffz",
		MinMemoryKBytes: defaultMinMemoryKB,
	}

	output.ServerID, err = strconv.Atoi(serverID)
	if err != nil {
		log.Fatal(err)
	}

	ourPublic, ourPrivate, err := box.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	output.OurPublicKey, output.OurPrivateKey = ourPublic[:], ourPrivate[:]

	if theirPublicStr != "" {
		reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(theirPublicStr))
		theirPublic, err := ioutil.ReadAll(reader)
		if err != nil {
			log.Fatal(err)
		}
		output.BackendPublicKey = theirPublic
	}

	bytes, err := json.MarshalIndent(output, "", "\t")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(bytes))
	err = ioutil.WriteFile(outputFile, bytes, 0600)
	if err != nil {
		log.Fatal(err)
	}
}
