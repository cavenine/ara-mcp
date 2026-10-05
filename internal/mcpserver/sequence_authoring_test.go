// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateSequenceSavesOpaqueBodyWithIntentKey(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","unknown":{"large":9007199254740993},"Items":{"$type":"System.Collections.ObjectModel.ObservableCollection` + "`" + `1[[OpenAstroAra.Sequencer.SequenceItem.ISequenceItem, OpenAstroAra.Sequencer]], System.ObjectModel","$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":120.0,"ImageType":"LIGHT","Binning":{"X":1,"Y":1}}]}}`
	)
	var creates int
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences":
			creates++
			if got := r.Header.Get("Idempotency-Key"); got != "create-intent-01" {
				t.Errorf("idempotency key = %q, want create intent ID", got)
			}
			requestBody, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read create body: %v", err)
			}
			if !bytes.Contains(requestBody, []byte(body)) {
				t.Errorf("Ara create body did not preserve opaque sequence JSON: %s", requestBody)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","name":"M31","body":`+body+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
			_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","name":"M31","body":`+body+`}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})

	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"create-intent-01","name":"M31","body":` + body + `}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("create_sequence result = %#v, error = %v", result, err)
	}
	if creates != 1 {
		t.Fatalf("Ara create requests = %d, want 1", creates)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(sequenceID)) || !bytes.Contains(encoded, []byte(`"outcome":"completed"`)) {
		t.Fatalf("create_sequence result missing saved detail/receipt: %s", encoded)
	}
	readBack, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "get_sequence", Arguments: json.RawMessage(`{"sequence_id":"` + sequenceID + `"}`),
	})
	if err != nil || readBack.IsError {
		t.Fatalf("get_sequence read-back = %#v, error = %v", readBack, err)
	}
	readBackJSON, err := json.Marshal(readBack.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(readBackJSON, []byte(`"$type":"OpenAstroAra.Sequencer.Container.SequentialContainer`)) ||
		!bytes.Contains(readBackJSON, []byte(`"unknown"`)) {
		t.Fatalf("sequence read-back lost opaque fields: %s", readBackJSON)
	}
	replayed, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"create-intent-01","name":"M31","body":` + body + `}`),
	})
	if err != nil || replayed.IsError {
		t.Fatalf("create_sequence replay = %#v, error = %v", replayed, err)
	}
	replayJSON, err := json.Marshal(replayed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if creates != 1 || !bytes.Contains(replayJSON, []byte(`"replayed":true`)) || !bytes.Contains(replayJSON, []byte(sequenceID)) {
		t.Fatalf("replayed create = %s, upstream creates = %d", replayJSON, creates)
	}
}

func TestCreateSequenceRejectsUnsupportedExecutableTypeBeforeSaving(t *testing.T) {
	const body = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"Vendor.Plugin.SequenceItem.RunScript, Vendor.Plugin"}]}}`
	var dispatched atomic.Bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences" {
			dispatched.Store(true)
		}
		http.NotFound(w, r)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"unsupported-01","name":"unsupported","body":` + body + `}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched.Load() {
		t.Fatalf("unsupported sequence result = %#v, upstream dispatched = %t; want rejection before save", result, dispatched.Load())
	}
}

func TestSavedSequenceLogCorrelatesIDWithoutBody(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":30}]},"private_note":"do not log this"}`
	)
	session, logs := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sequences" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","name":"M31"}`)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "create_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"log-create-01","name":"M31","body":` + body + `}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("create_sequence result = %#v, error = %v", result, err)
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"event":"sequence_saved"`)) || !bytes.Contains(logs.Bytes(), []byte(sequenceID)) {
		t.Fatalf("saved-plan log omitted event or sequence ID: %s", logs.String())
	}
	if bytes.Contains(logs.Bytes(), []byte("private_note")) || bytes.Contains(logs.Bytes(), []byte("do not log this")) {
		t.Fatalf("saved-plan log included sequence body: %s", logs.String())
	}
}

func TestUpdateSequenceSendsOnlyRequestedPatchFields(t *testing.T) {
	const sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/sequences/"+sequenceID {
			t.Errorf("request = %s %s, want PATCH /api/v1/sequences/%s", r.Method, r.URL.Path, sequenceID)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read patch body: %v", err)
		}
		if string(body) != `{"name":"Renamed"}` {
			t.Errorf("patch body = %s, want only the requested name", body)
		}
		_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","name":"Renamed","body":{}}`)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "update_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"update-01","sequence_id":"` + sequenceID + `","name":"Renamed"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("update_sequence result = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"name":"Renamed"`)) || !bytes.Contains(encoded, []byte(`"outcome":"completed"`)) {
		t.Fatalf("update_sequence result = %s", encoded)
	}
}

func TestUpdateSequenceReportsAraActiveRunConflict(t *testing.T) {
	const sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/sequences/"+sequenceID {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"title":"Sequence has an active run","detail":"Stop or abort the run first."}`)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "update_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"update-conflict-01","sequence_id":"` + sequenceID + `","name":"Renamed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("update_sequence result = %#v, want active-run conflict", result)
	}
	if len(result.Content) == 0 {
		t.Fatal("update_sequence conflict omitted error content")
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "active run") || !strings.Contains(text.Text, "Stop or abort the run first") {
		t.Fatalf("update_sequence error = %#v, want useful active-run reason", result.Content)
	}
}

func TestUpdateSequenceRejectsUnsupportedBodyBeforePatch(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"Vendor.Plugin.SequenceItem.RunScript, Vendor.Plugin"}]}}`
	)
	var dispatched atomic.Bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sequences/"+sequenceID {
			dispatched.Store(true)
		}
		http.NotFound(w, r)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "update_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"invalid-update-01","sequence_id":"` + sequenceID + `","body":` + body + `}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched.Load() {
		t.Fatalf("unsupported update result = %#v, Ara PATCH dispatched = %t", result, dispatched.Load())
	}
}

func TestInstantiateSequenceTemplateSavesWithoutStarting(t *testing.T) {
	const sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
	var instantiations int
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sequences/templates/lrgb-dso/instantiate" {
			t.Errorf("request = %s %s, want template instantiation", r.Method, r.URL.Path)
		}
		instantiations++
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("template instantiation used an unverified Ara idempotency key")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read instantiate body: %v", err)
		}
		if string(body) != `{"new_sequence_name":"LRGB M31","parameters":{"target_name":"M31"}}` {
			t.Errorf("instantiate body = %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","name":"LRGB M31","body":{"schemaVersion":"openastroara-sequence-v1"},"template_origin":"lrgb-dso"}`)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "instantiate_sequence_template",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"template-intent-01","template_name":"lrgb-dso","new_sequence_name":"LRGB M31","parameters":{"target_name":"M31"}}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("instantiate_sequence_template result = %#v, error = %v", result, err)
	}
	if instantiations != 1 {
		t.Fatalf("template instantiations = %d, want 1", instantiations)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(sequenceID)) || !bytes.Contains(encoded, []byte(`"outcome":"completed"`)) {
		t.Fatalf("template result = %s", encoded)
	}
}

func TestValidateSequenceDistinguishesStructuralValidityFromUnsupportedPalette(t *testing.T) {
	const body = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":30},{"$type":"Vendor.Plugin.SequenceItem.RunScript, Vendor.Plugin"}]}}`
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sequences/validate" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"valid":true}`)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "validate_sequence", Arguments: json.RawMessage(`{"body":` + body + `}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("validate_sequence result = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var validation SequenceValidation
	if err := json.Unmarshal(encoded, &validation); err != nil {
		t.Fatal(err)
	}
	if !validation.Valid || validation.ExecutableSupported || !strings.Contains(validation.SupportReason, "unsupported executable sequence type") {
		t.Fatalf("validation = %+v, want structurally valid but unsupported executable palette", validation)
	}
}

func TestSupportedSequencePaletteAcceptsBoundedLRGBBlock(t *testing.T) {
	body := jsontext.Value(`{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Conditions":{"$values":[{"$type":"OpenAstroAra.Sequencer.Conditions.LoopCondition, OpenAstroAra.Sequencer","Iterations":30}]},"Items":{"$values":[{"$type":"OpenAstroAra.Sequencer.SequenceItem.FilterWheel.SwitchFilter, OpenAstroAra.Sequencer","Filter":{"_name":"L","_position":0}},{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":120,"ImageType":"LIGHT","Binning":{"X":1,"Y":1}}]},"Triggers":{"$values":[]}}]}}`)
	if err := validateSupportedSequenceBody(body); err != nil {
		t.Fatalf("verified LRGB loop block rejected: %v", err)
	}
	tooMany := jsontext.Value(strings.Replace(string(body), `"Iterations":30`, `"Iterations":1001`, 1))
	if err := validateSupportedSequenceBody(tooMany); err == nil || !strings.Contains(err.Error(), "Iterations") {
		t.Fatalf("loop above authoring bound error = %v", err)
	}
	nested := `{"$type":"` + sequentialContainerType + `","Items":{"$values":[]}}`
	for range maxSequenceNestingDepth {
		nested = `{"$type":"` + sequentialContainerType + `","Items":{"$values":[` + nested + `]}}`
	}
	tooDeep := jsontext.Value(`{"schemaVersion":"openastroara-sequence-v1","$type":"` + sequentialContainerType + `","Items":{"$values":[` + nested + `]}}`)
	if err := validateSupportedSequenceBody(tooDeep); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("over-deep sequence error = %v", err)
	}
	item := `{"$type":"` + takeExposureType + `","ExposureTime":30}`
	items := strings.TrimSuffix(strings.Repeat(item+",", maxPlannedInstructions+1), ",")
	tooManyInstructions := jsontext.Value(`{"schemaVersion":"openastroara-sequence-v1","$type":"` + sequentialContainerType + `","Items":{"$values":[` + items + `]}}`)
	if err := validateSupportedSequenceBody(tooManyInstructions); err == nil || !strings.Contains(err.Error(), "planned instructions") {
		t.Fatalf("expanded sequence over budget error = %v", err)
	}
}

func newSequenceAuthoringTestSession(t *testing.T, upstreamHandler http.HandlerFunc) (*mcp.ClientSession, *bytes.Buffer) {
	t.Helper()
	const sessionHostname = "sequence-authoring-test"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/session":
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"`+sessionHostname+`","idle_seconds":1}`)
		default:
			upstreamHandler(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControlManager(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.stop)
	control.mu.Lock()
	control.phase = "owned"
	control.controlID = "control-01"
	control.identity = controlIdentity{
		serverUUID: "server-01", serverVersion: "1.0.0", apiVersion: "v1",
		daemonVersion: "1.0.0", daemonGitSHA: "build-01", apiSurfaces: []ara.APISurface{{Name: "rest", Version: "1.0.0"}},
		profileID: "profile-01", resumeToken: "0",
	}
	control.session = ara.ControlSession{Hostname: sessionHostname}
	control.mu.Unlock()

	var logs bytes.Buffer
	server, err := New(Options{Ara: client, Control: control, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession, &logs
}
