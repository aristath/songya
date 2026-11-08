package songya

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdatesPreserveRecordsAndRevisions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range destinations {
		t.Run(string(destination), func(t *testing.T) {
			original, err := store.Create(destination, testUserFields(destination, map[string]any{"title": "Before", "unknown": map[string]any{"keep": "Value"}}), "Body\r\n")
			if err != nil {
				t.Fatal(err)
			}
			created := original.Metadata().CreatedAt
			doc, err := store.Update(original.ID, original.Revision, map[string]any{"title": 3, "id": "wrong", "created_at": "wrong", "updated_at": "wrong", "bad": func() {}}, original.Body)
			if err != nil {
				t.Fatal(err)
			}
			read, err := store.Read(original.ID)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Pending || doc.ID != original.ID || doc.Revision == original.Revision || doc.Revision != read.Revision || read.Body != original.Body || read.Metadata().Title != "3" || !read.Metadata().CreatedAt.Equal(created) || read.Metadata().UpdatedAt.Before(created) || read.Metadata().UpdatedAt.Location() != time.UTC {
				t.Fatalf("update changed identity, timestamps or body: %+v", doc)
			}
			var unknown map[string]any
			if err := field(&read.Frontmatter, "unknown").Decode(&unknown); err != nil || unknown["keep"] != "Value" {
				t.Fatal("untouched metadata lost")
			}
			if len(read.Diagnostics()) == 0 {
				t.Fatal("bad leaf was not retained and diagnosed")
			}
			if _, err := store.Update(original.ID, original.Revision, nil, "Stale"); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale retry replaced the result: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(store.path, string(destination), doc.ID+"/record.md"))
			if err != nil {
				t.Fatal(err)
			}
			if dataRevision(data) != doc.Revision || bytes.Contains(data, []byte("revision:")) || bytes.Contains(data, []byte("id:")) {
				t.Fatal("revision is not an exact byte hash, or identity leaked into metadata")
			}
		})
	}
}

func TestUpdatesPreserveDamagedAndAliasedCreationTimes(t *testing.T) {
	for _, header := range []string{
		"title: Before\ncreated_at: unknown # Keep date comment\ncustom: &v [Keep]\ncopy: *v\n",
		"title: Before\ncreated_at: null\n",
		"title: Before\nupdated_at: &updated unknown\ncreated_at: *updated\n",
		"title: Before\n",
		"title: Before\ndate: &date 'winter of 06'\ncreated_at: *date\n",
		"title: Before\ndefaults: &defaults {created_at: unknown}\n<<: *defaults\n",
	} {
		t.Run(header, func(t *testing.T) {
			path := t.TempDir()
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			id := "m_000000000000"
			name, _ := recordName(id)
			if err := fixtureWriteFile(filepath.Join(path, name), []byte("---\nuser: test\n"+header+"---\nBody"), 0600); err != nil {
				t.Fatal(err)
			}
			original, err := store.Read(id)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := normalizeNode(metadataField(&original.Frontmatter, "created_at"))
			for i := 0; i < 3; i++ {
				changed, err := store.Update(id, original.Revision, map[string]any{"title": "After", "date": "Changed", "defaults": map[string]any{"created_at": "Changed"}}, original.Body)
				if err != nil {
					t.Fatal(err)
				}
				read, err := store.Read(id)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := normalizeNode(metadataField(&read.Frontmatter, "created_at"))
				// Compare the stored value, rather than parser source positions.
				var want, got any
				if err := before.Decode(&want); err != nil {
					t.Fatal(err)
				}
				if err := after.Decode(&got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(want, got) || read.Body != "Body" || read.Metadata().Title != "After" {
					t.Fatalf("creation value changed: %#v -> %#v", want, got)
				}
				data, err := read.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(header, "# Keep date comment") && !bytes.Contains(data, []byte("# Keep date comment")) {
					t.Fatal("date comment lost")
				}
				if strings.Contains(header, "custom: &v") && (!bytes.Contains(data, []byte("&v")) || !bytes.Contains(data, []byte("*v"))) {
					t.Fatal("unrelated alias lost")
				}
				original = changed
			}
		})
	}
}

func TestUpdatesProtectUserEditsAndMissingRecords(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Create(Memories, testUserFields(Memories, nil), "Before")
	if err != nil {
		t.Fatal(err)
	}
	name, _ := recordName(doc.ID)
	user := []byte("User edited this directly")
	if err := fixtureWriteFile(filepath.Join(path, name), user, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(doc.ID, doc.Revision, nil, "Stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("direct edit was overwritten: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(path, name)); err != nil || !bytes.Equal(data, user) {
		t.Fatal("user edit lost")
	}
	if err := os.Remove(filepath.Join(path, name)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(doc.ID, doc.Revision, nil, "Missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing record was recreated: %v", err)
	}
	if _, err := store.Update(doc.ID, "", nil, "Unknown revision"); err == nil {
		t.Fatal("unread base accepted")
	}
	if _, err := store.Update("../outside", doc.Revision, nil, "Invalid ID"); err == nil {
		t.Fatal("invalid identity accepted")
	}
	if _, err := (*Store)(nil).Update(doc.ID, doc.Revision, nil, "Nil store"); err == nil {
		t.Fatal("nil store accepted")
	}
}

func updateFixture(t *testing.T) (*Store, *os.Root, string, string, []byte) {
	t.Helper()
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	name := "memories/m_000000000000/record.md"
	if err := fixtureRootWriteFile(root, name, []byte(sample), 0600); err != nil {
		t.Fatal(err)
	}
	doc := Parse([]byte(sample))
	doc.Body = "Updated body"
	if err := doc.Set("title", "Updated"); err != nil {
		t.Fatal(err)
	}
	data, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return store, root, name, dataRevision([]byte(sample)), data
}

func TestUpdateRecoveryPreservesConflictsAndUnrelatedWork(t *testing.T) {
	for _, state := range []string{"staged", "written", "linked", "renamed", "newer", "missing", "corrupt", "ambiguous", "unknown", "publish-obstructed", "empty"} {
		t.Run(state, func(t *testing.T) {
			store, root, name, base, data := updateFixture(t)
			staged := name
			if state == "written" {
				if err := fixtureRootMkdir(root, pendingName(name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := fixtureRootWriteFile(root, temporaryName(name, base, data), data, 0600); err != nil {
					t.Fatal(err)
				}
			} else if retained, err := stage(root, staged, base, data); !retained || err != nil {
				t.Fatal(err)
			}
			ready := filepath.Join(pendingName(staged), "publish")
			switch state {
			case "linked", "renamed":
				if err := root.Link(temporaryName(staged, base, data), ready); err != nil {
					t.Fatal(err)
				}
				if state == "renamed" {
					if err := root.Rename(ready, name); err != nil {
						t.Fatal(err)
					}
				}
			case "newer":
				if err := fixtureRootWriteFile(root, name, []byte("Newer user version"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := root.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := fixtureRootWriteFile(root, temporaryName(staged, base, data), []byte("Retained partial"), 0600); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				if err := fixtureRootWriteFile(root, temporaryName(staged, base, []byte("Other proposal")), []byte("Other proposal"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown":
				if err := fixtureRootWriteFile(root, filepath.Join(pendingName(staged), "foreign.keep"), []byte("Unknown data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "publish-obstructed":
				if err := fixtureRootWriteFile(root, ready, []byte("Unknown data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := root.Remove(temporaryName(staged, base, data)); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := store.Read("m_000000000000")
			if state != "missing" && (readErr != nil || before.Pending) {
				t.Fatalf("pending proposal disabled committed read: %v", readErr)
			}
			if state != "renamed" && state != "newer" && state != "missing" && before.Body != Parse([]byte(sample)).Body {
				t.Fatal("proposal took precedence over committed content")
			}
			for range 2 {
				if err := store.Recover(); err != nil {
					t.Fatal(err)
				}
			}
			read, err := store.Read("m_000000000000")
			switch state {
			case "staged", "written", "linked", "renamed", "unknown":
				if err != nil || read.Body != "Updated body" || read.Revision != dataRevision(data) {
					t.Fatalf("update did not recover: %v", err)
				}
			case "newer":
				if err != nil || read.Body != "Newer user version" {
					t.Fatal("newer version overwritten")
				}
			case "missing":
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatal("missing canonical record was recreated")
				}
			default:
				if err != nil || read.Body != Parse([]byte(sample)).Body {
					t.Fatal("damaged update affected committed data")
				}
			}
			clean := state == "staged" || state == "written" || state == "linked" || state == "renamed" || state == "empty"
			if _, err := root.Lstat(pendingName(staged)); clean != errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("wrong cleanup in %s: %v", state, err)
			}
			if !clean && len(store.Diagnostics()) == 0 {
				t.Fatal("retained damage was not diagnosed")
			}
			if state == "unknown" || state == "publish-obstructed" {
				foreign := filepath.Join(pendingName(staged), "foreign.keep")
				if state == "publish-obstructed" {
					foreign = ready
				}
				if bytes, err := root.ReadFile(foreign); err != nil || string(bytes) != "Unknown data" {
					t.Fatal("unknown staging data discarded")
				}
			}
			// Other creation recovery and updates stay independent of this attempt.
			other := "entities/e_000000000000/record.md"
			if retained, err := stage(root, other, "", []byte("Other creation")); !retained || err != nil {
				t.Fatal(err)
			}
			if err := store.Recover(); err != nil {
				t.Fatal(err)
			}
			original, err := store.Read("e_000000000000")
			if err != nil || original.Pending {
				t.Fatalf("unrelated creation blocked: %v", err)
			}
			updated, err := store.Update(original.ID, original.Revision, nil, "Other update")
			if err != nil || updated.Pending {
				t.Fatalf("unrelated update blocked: %v", err)
			}
		})
	}
}

func TestUpdateReadersSeeCompleteVersions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("A", 128*1024), strings.Repeat("B", 128*1024)
	doc, err := store.Create(Memories, testUserFields(Memories, nil), a)
	if err != nil {
		t.Fatal(err)
	}
	id := doc.ID
	done := make(chan struct{})
	failures := make(chan error, 8)
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				read, err := store.Read(id)
				if err != nil {
					failures <- err
					return
				}
				if read.Body != a && read.Body != b {
					failures <- errors.New("reader saw partial replacement")
					return
				}
			}
		})
	}
	for i := 0; i < 20; i++ {
		body := a
		if i%2 == 0 {
			body = b
		}
		updated, err := store.Update(id, doc.Revision, nil, body)
		if err != nil {
			t.Error(err)
			break
		}
		doc = updated
	}
	close(done)
	readers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestConcurrentUpdateProcessesChooseOneRevision(t *testing.T) {
	store, _, _, base, _ := updateFixture(t)
	results := make(chan string, 4)
	for range 4 {
		command := helperProcess(t, "update-writer", store.path)
		command.Env = append(command.Env, "SONGYA_TEST_REVISION="+base)
		go func() {
			out, err := command.CombinedOutput()
			if err != nil {
				results <- fmt.Sprintf("error: %v %s", err, out)
			} else {
				results <- string(out)
			}
		}()
	}
	winners := 0
	for range 4 {
		result := <-results
		if strings.Contains(result, "updated") {
			winners++
		} else if !strings.Contains(result, "conflict") {
			t.Fatal(result)
		}
	}
	if winners != 1 {
		t.Fatalf("expected one winner; got %d", winners)
	}
	entries, err := os.ReadDir(filepath.Join(store.path, "memories"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("updates duplicated records or left conflicting staging: %v", err)
	}
}

func TestUpdateCrashRecovery(t *testing.T) {
	for _, point := range []string{"staged", "linked", "renamed"} {
		t.Run(point, func(t *testing.T) {
			store, _, _, _, _ := updateFixture(t)
			command := helperProcess(t, "update-crash-"+point, store.path)
			stdout, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { command.Process.Kill(); command.Wait() }()
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("did not reach crash point: %q, %v", line, err)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()
			reopened, err := Open(store.path)
			if err != nil {
				t.Fatal(err)
			}
			read, err := reopened.Read("m_000000000000")
			if err != nil || read.Body != "Recovered update" || read.Pending || len(reopened.Diagnostics()) != 0 {
				t.Fatalf("crash recovery failed: %v, %v", err, reopened.Diagnostics())
			}
		})
	}
}

func testUpdateProcess(t *testing.T, role, path string) {
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := "m_000000000000"
	if role == "update-orphan-collision" {
		previous := rand.Reader
		rand.Reader = bytes.NewReader(append(make([]byte, IDSuffixLength), bytes.Repeat([]byte{1}, 512)...))
		defer func() { rand.Reader = previous }()
		doc, err := store.Create(Memories, testUserFields(Memories, nil), "New identity")
		if err != nil || doc.ID == id {
			t.Fatalf("orphaned update lost its identity: %v", err)
		}
		return
	}
	if role == "update-writer" {
		_, err := store.Update(id, os.Getenv("SONGYA_TEST_REVISION"), nil, fmt.Sprintf("Writer %d", os.Getpid()))
		if errors.Is(err, ErrConflict) {
			fmt.Println("conflict")
		} else if err != nil {
			t.Fatal(err)
		} else {
			fmt.Println("updated")
		}
		return
	}
	doc, err := store.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	doc.Body = "Recovered update"
	data, err := doc.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	err = store.locked(true, func(root *os.Root) error {
		name, _ := recordName(id)
		staged := name
		if _, err := stage(root, staged, doc.Revision, data); err != nil {
			return err
		}
		ready := filepath.Join(pendingName(staged), "publish")
		if role != "update-crash-staged" {
			if err := root.Link(temporaryName(staged, doc.Revision, data), ready); err != nil {
				return err
			}
		}
		if role == "update-crash-renamed" {
			if err := root.Rename(ready, name); err != nil {
				return err
			}
		}
		fmt.Println("ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpdateFinishesCreationCleanupWithoutStaleDiagnostics(t *testing.T) {
	store, root, name, _, _ := updateFixture(t)
	if retained, err := stage(root, name, "", []byte(sample)); !retained || err != nil {
		t.Fatal(err)
	}
	original, err := store.Read("m_000000000000")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(original.ID, original.Revision, nil, "Updated body")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(updated.Diagnostics(), "\n"), "Pending work") {
		t.Fatal("finished creation staging stayed reported as pending")
	}
}

func TestUpdatePublicationKeepsPayloadUntilDirectorySynced(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	store, root, name, base, data := updateFixture(t)
	if retained, err := stage(root, name, base, data); !retained || err != nil {
		t.Fatal(err)
	}
	publicationRoot, err := os.OpenRoot(filepath.Join(store.path, filepath.Dir(name)))
	if err != nil {
		t.Fatal(err)
	}
	defer publicationRoot.Close()
	if err := root.Chmod(filepath.Dir(name), 0300); err != nil {
		t.Fatal(err)
	}
	defer root.Chmod(filepath.Dir(name), 0700)
	if published, err := store.publishUpdate(publicationRoot, filepath.Base(name), base, data); published || err == nil {
		t.Fatal("acknowledged an update without syncing its destination")
	}
	if err := root.Chmod(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if actual, err := root.ReadFile(name); err != nil || !bytes.Equal(actual, data) {
		t.Fatal("test did not reach replacement")
	}
	if retained, err := root.ReadFile(temporaryName(name, base, data)); err != nil || !bytes.Equal(retained, data) {
		t.Fatal("unsynced publication lost its staged payload")
	}

	if err := store.Recover(); err != nil || len(store.Diagnostics()) != 0 {
		t.Fatalf("publication did not resume: %v, %v", err, store.Diagnostics())
	}
}

func TestOrphanedUpdatesKeepTheirIdentity(t *testing.T) {
	store, root, name, base, data := updateFixture(t)
	if retained, err := stage(root, name, base, data); !retained || err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(name); err != nil {
		t.Fatal(err)
	}
	if output, err := helperProcess(t, "update-orphan-collision", store.path).CombinedOutput(); err != nil {
		t.Fatalf("creation reused an update's identity: %v\n%s", err, output)
	}
	if retained, err := root.ReadFile(temporaryName(name, base, data)); err != nil || !bytes.Equal(retained, data) {
		t.Fatal("orphaned proposal was discarded")
	}
}

func TestUpdateCanRetryAfterStagingPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	store, root, _, _, _ := updateFixture(t)
	original, err := store.Read("m_000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Chmod("memories/m_000000000000", 0500); err != nil {
		t.Fatal(err)
	}
	defer root.Chmod("memories/m_000000000000", 0700)
	failed, err := store.Update(original.ID, original.Revision, nil, "Retry")
	if err == nil || failed.Pending || failed.Revision != original.Revision || failed.Body != "Retry" {
		t.Fatal("failed staging claimed a retained or committed proposal")
	}
	read, err := store.Read(original.ID)
	if err != nil || read.Body != original.Body {
		t.Fatal("failed staging damaged committed content")
	}
	if err := root.Chmod("memories/m_000000000000", 0700); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(original.ID, original.Revision, nil, "Retry")
	if err != nil || updated.Pending || updated.Body != "Retry" || len(store.Diagnostics()) != 0 {
		t.Fatalf("retry stayed blocked: %v, %v", err, store.Diagnostics())
	}
}

func TestRecoveringPriorUpdateDoesNotLeaveAFalseConflictIssue(t *testing.T) {
	store, root, name, base, data := updateFixture(t)
	if retained, err := stage(root, name, base, data); !retained || err != nil {
		t.Fatal(err)
	}
	proposal, err := store.Update("m_000000000000", base, nil, "Later request")
	if !errors.Is(err, ErrConflict) || proposal.Pending {
		t.Fatalf("a prior recovered update was overwritten or claimed: %v", err)
	}
	read, err := store.Read("m_000000000000")
	if err != nil || read.Body != "Updated body" {
		t.Fatal("the prior proposal did not recover")
	}
	if len(store.Diagnostics()) != 0 {
		t.Fatalf("a rejected, unstaged request became a recovery issue: %v", store.Diagnostics())
	}
	if strings.Contains(strings.Join(proposal.Diagnostics(), "\n"), pendingWarning) {
		t.Fatal("the returned proposal still claims prior recovered work is pending")
	}
}

func TestUntouchedCreationAliasesSurviveAnchorRepair(t *testing.T) {
	for _, value := range []string{"'winter of 06'", "{description: 'winter of 06', nested: &loop [*loop]}"} {
		store, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		id := "m_000000000000"
		name, _ := recordName(id)
		source := []byte("---\nuser: test\nother: &date First\ndate: &date " + value + "\ncreated_at: *date\n---\nBody")
		if err := fixtureWriteFile(filepath.Join(store.path, name), source, 0600); err != nil {
			t.Fatal(err)
		}
		original, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Update(id, original.Revision, map[string]any{"title": "Edited"}, original.Body); err != nil {
			t.Fatal(err)
		}
		read, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		created := field(&read.Frontmatter, "created_at")
		if created.Alias != field(&read.Frontmatter, "date") {
			t.Fatal("an unrelated update detached an unchanged creation alias")
		}
	}
}
