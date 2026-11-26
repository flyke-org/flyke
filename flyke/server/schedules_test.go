package server_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/flyke-org/flyke/api/rest/v1"
)

func TestSchedulesOverREST(t *testing.T) {
	h := start(t)
	const base = "/api/v1/namespaces/default/schedules"

	// Preview explains an expression before anything is saved.
	var preview v1.SchedulePreview
	code := h.rest(http.MethodPost, base+"/preview", v1.SchedulePreviewRequest{Cron: "0 9 * * 1-5", TZ: "Europe/Paris"}, &preview)
	if code != http.StatusOK || preview.Description != "Monday to Friday at 09:00 (Europe/Paris)" || len(preview.Upcoming) != 5 {
		t.Fatalf("preview: %d %+v", code, preview)
	}
	var st v1.Status
	if code := h.rest(http.MethodPost, base+"/preview", v1.SchedulePreviewRequest{Cron: "every day"}, &st); code != http.StatusUnprocessableEntity || !strings.Contains(st.Message, "5 fields") {
		t.Fatalf("invalid preview: %d %q", code, st.Message)
	}

	spec := v1.ScheduleSpec{Workflow: "Pipeline", Cron: "@every 1h", Inputs: map[string]any{"key": "nightly-{schedule.date}"}}
	var sc v1.Schedule
	if code := h.rest(http.MethodPut, base+"/nightly", spec, &sc); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if sc.Status.Description != "Every hour" || sc.Status.NextRunAt == nil || time.Until(*sc.Status.NextRunAt) > time.Hour {
		t.Fatalf("created: %+v", sc.Status)
	}
	spec.Cron = "30 6 * * *"
	if code := h.rest(http.MethodPut, base+"/nightly", spec, &sc); code != http.StatusOK || sc.Spec.Cron != "30 6 * * *" {
		t.Fatalf("update: %d %+v", code, sc.Spec)
	}
	// A schedule whose inputs the workflow would reject is refused up front.
	bad := v1.ScheduleSpec{Workflow: "Pipeline", Cron: "@daily"}
	if code := h.rest(http.MethodPut, base+"/broken", bad, &st); code != http.StatusUnprocessableEntity {
		t.Fatalf("schedule without the required input: %d", code)
	}

	var list v1.List[v1.Schedule]
	if h.rest(http.MethodGet, base+"?workflow=Pipeline", nil, &list); len(list.Items) != 1 || list.Items[0].Metadata.Name != "nightly" {
		t.Fatalf("list: %+v", list.Items)
	}
	if h.rest(http.MethodGet, base+"?workflow=Other", nil, &list); len(list.Items) != 0 {
		t.Fatalf("list of another workflow: %+v", list.Items)
	}

	sc = v1.Schedule{} // omitted fields must not survive from the previous response
	if code := h.rest(http.MethodPost, base+"/nightly/pause", nil, &sc); code != http.StatusOK || !sc.Spec.Paused || sc.Status.NextRunAt != nil {
		t.Fatalf("pause: %d %+v", code, sc)
	}
	var r v1.Run
	if code := h.rest(http.MethodPost, base+"/nightly/trigger", nil, &r); code != http.StatusCreated || r.Spec.TriggeredBy != "schedule:nightly" {
		t.Fatalf("trigger: %d %+v", code, r.Spec)
	}
	if !strings.Contains(string(r.Spec.Inputs), `"nightly-20`) {
		t.Fatalf("inputs: %s", r.Spec.Inputs)
	}
	sc = v1.Schedule{}
	if code := h.rest(http.MethodPost, base+"/nightly/resume", nil, &sc); code != http.StatusOK || sc.Spec.Paused || sc.Status.LastRun != r.Metadata.Name || len(sc.Status.Upcoming) != 5 {
		t.Fatalf("resume: %d %+v", code, sc)
	}

	if code := h.rest(http.MethodDelete, base+"/nightly", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	if code := h.rest(http.MethodGet, base+"/nightly", nil, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: %d", code)
	}
}

// The controller sleeps until the next tick; saving a schedule wakes it, and
// the run starts when the tick arrives without any polling in between.
func TestScheduleControllerFiresDueTick(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a real 10s tick")
	}
	h := start(t)
	spec := v1.ScheduleSpec{Workflow: "Pipeline", Cron: "@every 10s", Inputs: map[string]any{"key": "tick"}}
	if code := h.rest(http.MethodPut, "/api/v1/namespaces/default/schedules/fast", spec, nil); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var runs v1.List[v1.Run]
		h.rest(http.MethodGet, "/api/v1/namespaces/default/runs", nil, &runs)
		if len(runs.Items) > 0 {
			if got := runs.Items[0].Spec.TriggeredBy; got != "schedule:fast" {
				t.Fatalf("triggeredBy %q", got)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the schedule did not start a run within 20s")
}
