// The demo runs synthetic requests against an ephemeral loopback provider.
// It does not load configuration, use API keys, or persist any data.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/event"
	"github.com/AndrewFribush/tokenpylon-recorder-core/internal/proxy"
)

const promptMarker = "SYNTHETIC_PROMPT_DO_NOT_RECORD"
const replyMarker = "SYNTHETIC_REPLY_DO_NOT_RECORD"

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out io.Writer) error {
	upstream := httptest.NewServer(http.HandlerFunc(syntheticProvider))
	defer upstream.Close()
	upURL, err := url.Parse(upstream.URL)
	if err != nil {
		return err
	}
	allowedPath := "/proxy/" + upURL.Host + "/v1/chat/completions"
	recorded := make(chan *event.Event, 4)
	px := proxy.New(proxy.Options{
		InstallID: "r_0123456789abcdef", AllowPrivate: true,
		Sink: func(e *event.Event) { recorded <- e },
	})
	// Expose exactly our synthetic provider path. The extracted library also
	// supports real providers, but those routes are unreachable in this demo.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != allowedPath || r.URL.RawQuery != "" {
			http.Error(w, "synthetic provider only", http.StatusForbidden)
			return
		}
		px.ServeHTTP(w, r)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		return err
	}
	// The client can dial only this demo's proxy listener, never an external
	// host or an environment-configured HTTP proxy. Redirects are refused.
	transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != serverURL.Host {
			return nil, fmt.Errorf("demo refuses destination %q", addr)
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, addr)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	for _, kind := range []string{"json", "stream", "unknown", "cancel"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		body := fmt.Sprintf(`{"model":"demo-%s","stream":%t,"messages":[{"role":"user","content":%q}]}`, kind, kind == "stream" || kind == "cancel", promptMarker)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+allowedPath, strings.NewReader(body))
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			cancel()
			return err
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			cancel()
			return fmt.Errorf("%s: synthetic provider returned %d", kind, res.StatusCode)
		}
		var response []byte
		if kind == "cancel" {
			response, err = bufio.NewReader(res.Body).ReadBytes('\n')
			cancel()
		} else {
			response, err = io.ReadAll(res.Body)
		}
		res.Body.Close()
		cancel()
		if err != nil || !bytes.Contains(response, []byte(replyMarker)) {
			return fmt.Errorf("%s: synthetic response was not forwarded (read error: %v)", kind, err)
		}
		var e *event.Event
		select {
		case e = <-recorded:
		case <-time.After(5 * time.Second):
			return fmt.Errorf("%s: no usage event recorded", kind)
		}
		if e.RequestedModel != "demo-"+kind || e.Provider != "local" {
			return fmt.Errorf("%s: wrong event identity", kind)
		}
		if kind == "json" || kind == "stream" {
			if e.InputTokens == nil || *e.InputTokens != 8 || e.CachedTokens == nil || *e.CachedTokens != 4 || e.OutputTokens == nil || *e.OutputTokens != 3 || !e.Complete {
				return fmt.Errorf("%s: expected 8 uncached input, 4 cached, 3 output tokens", kind)
			}
		} else if e.InputTokens != nil || e.OutputTokens != nil {
			return fmt.Errorf("%s: missing usage must remain unknown", kind)
		}
		if kind == "cancel" && (!e.Cancelled || e.Complete) {
			return fmt.Errorf("cancel: expected cancelled, incomplete event")
		}
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if bytes.Contains(payload, []byte(promptMarker)) || bytes.Contains(payload, []byte(replyMarker)) {
			return fmt.Errorf("%s: message content leaked into event", kind)
		}
		fmt.Fprintf(out, "%s response: %s\n%s event: %s\n", kind, response, kind, payload)
	}
	px.Wait()
	fmt.Fprintln(out, "PASS: four synthetic local calls; message content absent from events; no files or uploads.")
	return nil
}

func syntheticProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "bad synthetic request", http.StatusBadRequest)
		return
	}
	if req.Model == "demo-json" || req.Model == "demo-unknown" {
		w.Header().Set("Content-Type", "application/json")
		usage := ""
		if req.Model == "demo-json" {
			usage = `,"usage":{"prompt_tokens":12,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens":3}`
		}
		fmt.Fprintf(w, `{"id":"synthetic-%s","model":%q,"choices":[{"message":{"content":%q}}]%s}`, req.Model, req.Model, replyMarker, usage)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"id\":\"synthetic-%s\",\"model\":%q,\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", req.Model, req.Model, replyMarker)
	w.(http.Flusher).Flush()
	if req.Model == "demo-cancel" {
		<-r.Context().Done()
		return
	}
	fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":12,\"prompt_tokens_details\":{\"cached_tokens\":4},\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
}
