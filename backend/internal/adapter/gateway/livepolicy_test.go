package gateway_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zoikogroup/zoikotax/backend/internal/adapter/gateway"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/ai"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/errs"
	"github.com/zoikogroup/zoikotax/backend/internal/domain/privacy"
)

// registryJSON is a registry file with two use cases. extra is spliced in after
// the use cases: the kill state, or nothing.
func registryJSON(extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"use_cases":[` +
		`{"use_case_id":"classification-review","owner":"lane-l","description":"d","max_risk_tier":"T2","max_authority":"A1",` +
		`"permitted_regions":["` + cell + `"],"permitted_data_classes":["P0","P1","P5"],"route":{}},` +
		`{"use_case_id":"explain-obligation-row","owner":"lane-l","description":"d","max_risk_tier":"T1","max_authority":"A1",` +
		`"permitted_regions":["` + cell + `"],"permitted_data_classes":["P0"],"route":{}}]` + extra + `}`
}

// livePolicy writes a registry file in a temporary folder and loads it.
func livePolicy(t *testing.T, extra string) (*gateway.FilePolicy, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.json")
	writeFile(t, path, registryJSON(extra))
	fp, err := gateway.NewFilePolicy(path, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return fp, path
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// killedNow reports whether a use case is refused by the current snapshot.
func killedNow(fp *gateway.FilePolicy, uc ai.AiUseCase) bool { return fp.Policy().Killed(uc) }

const classify, explain = ai.AiUseCase("classification-review"), ai.AiUseCase("explain-obligation-row")

func TestAFilePolicyStartsAsTheFileSays(t *testing.T) {
	fp, _ := livePolicy(t, `"killed":["explain-obligation-row"],"global_kill":false`)
	if killedNow(fp, classify) || !killedNow(fp, explain) {
		t.Fatal("the start-up kill state is not the file's")
	}
}

// A bad file at start stops the process, as LoadPolicy does.
func TestAFilePolicyRefusesABadFileAtStart(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	for name, path := range map[string]string{"missing": missing} {
		if _, err := gateway.NewFilePolicy(path, nil); err == nil {
			t.Fatalf("%s: started", name)
		}
	}
	for name, body := range map[string]string{
		"empty":         ``,
		"not JSON":      `{`,
		"unknown field": `{"use_cases":[],"surprise":true}`,
		"no route":      `{"use_cases":[{"use_case_id":"x","owner":"o","description":"d","max_risk_tier":"T1","max_authority":"A1"}]}`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
		writeFile(t, path, body)
		if _, err := gateway.NewFilePolicy(path, nil); err == nil {
			t.Fatalf("%s: started", name)
		}
	}
}

// Allowed, then flipped to killed: the next call is refused and never reaches
// the transport. Then restored: the next call goes through.
func TestAKillInTheFileTakesEffectOnTheNextCallAndCanBeRestored(t *testing.T) {
	fp, path := livePolicy(t, "")
	h := newHarness(t, func(c *gateway.Config) { c.Policy = fp })

	if _, err := h.client.Suggest(tenantCtx(), invocation()); err != nil {
		t.Fatalf("allowed call: %v", err)
	}
	if len(h.transport.Calls()) != 1 {
		t.Fatalf("calls = %d, want 1", len(h.transport.Calls()))
	}

	writeFile(t, path, registryJSON(`"global_kill":true`))
	_, err := h.client.Suggest(tenantCtx(), invocation())
	wantReason(t, err, ai.ReasonKillSwitchEngaged, errs.CategoryPolicy)
	if len(h.transport.Calls()) != 1 {
		t.Fatal("a killed call reached the Gateway")
	}

	writeFile(t, path, registryJSON(`"global_kill":false`))
	if _, err := h.client.Suggest(tenantCtx(), invocation()); err != nil {
		t.Fatalf("restored call: %v", err)
	}
	if len(h.transport.Calls()) != 2 {
		t.Fatalf("calls = %d, want 2", len(h.transport.Calls()))
	}
}

// Killing one use case leaves the other working.
func TestKillingOneUseCaseInTheFileLeavesTheOtherWorking(t *testing.T) {
	fp, path := livePolicy(t, "")
	writeFile(t, path, registryJSON(`"killed":["classification-review"]`))
	if !killedNow(fp, classify) || killedNow(fp, explain) {
		t.Fatal("only classification-review should be killed")
	}
	writeFile(t, path, registryJSON(`"killed":[]`))
	if killedNow(fp, classify) || killedNow(fp, explain) {
		t.Fatal("nothing should be killed")
	}
}

// A kill edit that keeps the file's size, and its modification time, is still
// read on the next call: there is no stat shortcut.
func TestAKillEditThatKeepsTheSizeAndTheTimeStillTakesEffect(t *testing.T) {
	fp, path := livePolicy(t, `"killed":["a"]`)
	if !killedNow(fp, "a") || killedNow(fp, "b") {
		t.Fatal("start state")
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, registryJSON(`"killed":["b"]`))
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("the edit changed the size or the time (%d to %d)", before.Size(), after.Size())
	}
	if killedNow(fp, "a") || !killedNow(fp, "b") {
		t.Fatal("an edit with the same size and time was not read")
	}
}

// The file deleted: everything is killed. Put back: it recovers, even with the
// same bytes as before the deletion.
func TestADeletedFileKillsEverythingAndRecoversWhenItReturns(t *testing.T) {
	fp, path := livePolicy(t, `"killed":[]`)
	good := registryJSON(`"killed":[]`)
	if killedNow(fp, classify) {
		t.Fatal("start state")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if !killedNow(fp, classify) || !killedNow(fp, explain) || !killedNow(fp, "anything") {
		t.Fatal("a deleted file did not kill everything")
	}
	writeFile(t, path, good)
	if killedNow(fp, classify) || killedNow(fp, explain) {
		t.Fatal("the same bytes put back did not recover")
	}
}

// A path that is a directory cannot be read, and counts as unreadable.
func TestADirectoryInPlaceOfTheFileKillsEverything(t *testing.T) {
	fp, path := livePolicy(t, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Skipf("cannot put a directory in place of the file here: %v", err)
	}
	if !killedNow(fp, classify) {
		t.Fatal("an unreadable path did not kill")
	}
}

// Every kind of unreadable content kills everything, and a repaired file with
// the very same bytes as the last good one recovers on the next call.
func TestAFileThatCannotBeReadAsKillStateKillsEverythingAndRecovers(t *testing.T) {
	bad := map[string]string{
		"empty file":                 ``,
		"only spaces":                `   `,
		"not JSON":                   `{"use_cases":`,
		"corrupted":                  "\x00\x01\x02 not a registry",
		"an array":                   `[]`,
		"null":                       `null`,
		"a string":                   `"x"`,
		"global_kill a string":       registryJSON(`"global_kill":"yes"`),
		"global_kill a number":       registryJSON(`"global_kill":1`),
		"global_kill null":           registryJSON(`"global_kill":null`),
		"killed a string":            registryJSON(`"killed":"classification-review"`),
		"killed an object":           registryJSON(`"killed":{}`),
		"killed null":                registryJSON(`"killed":null`),
		"killed has an empty string": registryJSON(`"killed":[""]`),
		"killed has a number":        registryJSON(`"killed":[1]`),
		"killed has null":            registryJSON(`"killed":[null]`),
		"not UTF-8":                  registryJSON("\"killed\":[\"\xff\"]"),
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			fp, path := livePolicy(t, `"killed":[]`)
			good := registryJSON(`"killed":[]`)
			if killedNow(fp, classify) {
				t.Fatal("start state")
			}
			writeFile(t, path, body)
			if !killedNow(fp, classify) || !killedNow(fp, explain) {
				t.Fatalf("%s did not kill everything", name)
			}
			// Repaired with the same bytes as the last good file.
			writeFile(t, path, good)
			if killedNow(fp, classify) || killedNow(fp, explain) {
				t.Fatalf("%s: a repaired file did not recover", name)
			}
		})
	}
}

// A missing key means "not killed", as in the Python Gateway.
func TestMissingKillKeysMeanNotKilled(t *testing.T) {
	fp, path := livePolicy(t, `"killed":["classification-review"],"global_kill":true`)
	if !killedNow(fp, classify) {
		t.Fatal("start state")
	}
	writeFile(t, path, registryJSON(""))
	if killedNow(fp, classify) || killedNow(fp, explain) {
		t.Fatal("a file with neither key killed something")
	}
	writeFile(t, path, registryJSON(`"global_kill":true`))
	if !killedNow(fp, classify) {
		t.Fatal("global_kill alone did not kill")
	}
	writeFile(t, path, registryJSON(`"killed":["explain-obligation-row"]`))
	if killedNow(fp, classify) || !killedNow(fp, explain) {
		t.Fatal("killed alone did not kill only the one")
	}
}

// A file replaced by rename (a new inode) is read by path: the old file is not
// held open.
func TestAFileReplacedByRenameIsRead(t *testing.T) {
	fp, path := livePolicy(t, "")
	if killedNow(fp, classify) {
		t.Fatal("start state")
	}
	replacement := path + ".new"
	writeFile(t, replacement, registryJSON(`"global_kill":true`))
	if err := os.Rename(replacement, path); err != nil {
		t.Skipf("this system refuses to rename over an existing file: %v", err)
	}
	if !killedNow(fp, classify) {
		t.Fatal("a file replaced by rename was not read")
	}
	replacement2 := path + ".new2"
	writeFile(t, replacement2, registryJSON(`"global_kill":false`))
	if err := os.Rename(replacement2, path); err != nil {
		t.Skipf("this system refuses to rename over an existing file: %v", err)
	}
	if killedNow(fp, classify) {
		t.Fatal("a second replacement was not read")
	}
}

// A live edit can narrow what a call may do and can never widen it: the use
// cases, ceilings, data classes and suspended flag are the ones loaded at start.
func TestALiveEditCannotWidenWhatACallMayDo(t *testing.T) {
	fp, path := livePolicy(t, "")
	wide := `{"use_cases":[` +
		`{"use_case_id":"classification-review","owner":"lane-l","description":"d","max_risk_tier":"T3","max_authority":"A3",` +
		`"permitted_regions":["` + cell + `","anywhere"],"permitted_data_classes":["P0","P1","P5","P6","P7"],"suspended":false,"route":{}},` +
		`{"use_case_id":"explain-obligation-row","owner":"lane-l","description":"d","max_risk_tier":"T3","max_authority":"A3",` +
		`"permitted_regions":["` + cell + `"],"permitted_data_classes":["P0","P5"],"route":{}},` +
		`{"use_case_id":"brand-new","owner":"lane-l","description":"d","max_risk_tier":"T1","max_authority":"A1",` +
		`"permitted_regions":["` + cell + `"],"permitted_data_classes":["P0"],"route":{}}],"killed":[]}`
	writeFile(t, path, wide)
	p := fp.Policy()

	if uc, ok := p.UseCase(explain); !ok || len(uc.PermittedDataClasses) != 1 || uc.PermittedDataClasses[0] != privacy.P0 ||
		uc.MaxRiskTier != ai.RiskTierT1 {
		t.Fatalf("explain was widened: %+v", uc)
	}
	if uc, ok := p.UseCase(classify); !ok || uc.MaxRiskTier != ai.RiskTierT2 || uc.MaxAuthority != ai.AuthorityOutcomeA1 ||
		len(uc.PermittedRegions) != 1 || len(uc.PermittedDataClasses) != 3 {
		t.Fatalf("classification was widened: %+v", uc)
	}
	if _, ok := p.UseCase("brand-new"); ok {
		t.Fatal("a use case added to the file while running was registered")
	}

	// And a call that the start-up policy refuses is still refused: the explain
	// use case may not carry P5.
	h := newHarness(t, func(c *gateway.Config) { c.Policy = fp })
	inv := invocation()
	inv.UseCase = explain
	inv.RiskTier = ai.RiskTierT1
	inv.DataClasses = []privacy.Class{privacy.P0, privacy.P5}
	_, err := h.client.Suggest(tenantCtx(), inv)
	wantReason(t, err, ai.ReasonDataClassRefused, errs.CategoryPolicy)
}

// A use case that was suspended at start stays suspended, whatever the file says
// later; a kill is the only thing that follows the file.
func TestSuspendedStaysAsLoadedAtStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	suspended := strings.Replace(registryJSON(""), `"route":{}}`, `"suspended":true,"route":{}}`, 1)
	writeFile(t, path, suspended)
	fp, err := gateway.NewFilePolicy(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, registryJSON(""))
	uc, ok := fp.Policy().UseCase(classify)
	if !ok || !uc.Suspended {
		t.Fatalf("suspended was changed by a live edit: %+v", uc)
	}
}

// Many callers while the file changes underneath them. Every snapshot is a
// whole one: both use cases are always there, and a kill is on or off, never a
// half. When the writer stops, the last file written is what is read. (Run under
// -race in the container: this machine's Go does not support it.)
func TestConcurrentCallsWhileTheFileChanges(t *testing.T) {
	fp, path := livePolicy(t, "")
	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		bodies := []string{registryJSON(`"global_kill":true`), registryJSON(`"killed":["classification-review"]`), registryJSON("")}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
			tmp := fmt.Sprintf("%s.%d", path, i)
			if err := os.WriteFile(tmp, []byte(bodies[i%len(bodies)]), 0o600); err != nil {
				return
			}
			if err := os.Rename(tmp, path); err != nil {
				_ = os.Remove(tmp)
				// A rename that fails (a sharing violation on some systems) is not
				// the thing under test; write in place instead.
				_ = os.WriteFile(path, []byte(bodies[i%len(bodies)]), 0o600)
			}
		}
	}()

	var wg sync.WaitGroup
	errc := make(chan string, 64)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				p := fp.Policy()
				if _, ok := p.UseCase(classify); !ok {
					errc <- "a snapshot lost a use case"
					return
				}
				if _, ok := p.UseCase(explain); !ok {
					errc <- "a snapshot lost a use case"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	writer.Wait()
	close(errc)
	for msg := range errc {
		t.Fatal(msg)
	}

	// The writer has stopped: the file as it stands is now what is served.
	writeFile(t, path, registryJSON(`"global_kill":true`))
	if !killedNow(fp, classify) {
		t.Fatal("the last write was not read")
	}
	writeFile(t, path, registryJSON(""))
	if killedNow(fp, classify) {
		t.Fatal("the last write was not read")
	}
}

// A state change is logged once, however many calls see it, and the file's
// content never reaches the log.
func TestAStateChangeIsLoggedOnceAndNeverWithTheContent(t *testing.T) {
	var buf lockedBuffer
	path := filepath.Join(t.TempDir(), "registry.json")
	writeFile(t, path, registryJSON(""))
	fp, err := gateway.NewFilePolicy(path, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "SECRET-MARKER-9f3a"
	for i := 0; i < 5; i++ {
		killedNow(fp, classify)
	}
	if buf.count("cannot be used") != 0 {
		t.Fatalf("logged while healthy: %q", buf.String())
	}

	writeFile(t, path, marker+" {")
	for i := 0; i < 5; i++ {
		killedNow(fp, classify)
	}
	if got := buf.count("cannot be used"); got != 1 {
		t.Fatalf("an unparseable file was logged %d times in 5 calls, want 1: %q", got, buf.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		killedNow(fp, classify)
	}
	if got := buf.count("cannot be used"); got != 2 {
		t.Fatalf("a change to unreadable was logged %d times in all, want 2 (one per state): %q", got, buf.String())
	}
	if got := buf.count("state=unreadable"); got != 1 {
		t.Fatalf("unreadable logged %d times, want 1", got)
	}

	writeFile(t, path, registryJSON(""))
	for i := 0; i < 5; i++ {
		killedNow(fp, classify)
	}
	if got := buf.count("readable again"); got != 1 {
		t.Fatalf("recovery was logged %d times, want 1: %q", got, buf.String())
	}
	if strings.Contains(buf.String(), marker) || strings.Contains(buf.String(), path) {
		t.Fatalf("the log carries the file's content or path: %q", buf.String())
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedBuffer) count(s string) int { return strings.Count(l.String(), s) }
