package songya

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func batchProcessRequest() BatchRequest {
	return BatchRequest{User: "test", Key: "process-capture", Account: "Original account", Changes: []Change{{Ref: "one", Body: textPointer("One")}, {Ref: "two", Body: textPointer("Two"), Fields: map[string]any{"relations": []Relation{{"related_to", "$one"}}}}}}
}

func testBatchProcess(t *testing.T, role, path string) {
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	request := batchProcessRequest()
	if role == "batch-retry" {
		receipt, err := store.ApplyBatch(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("identity:%s,%s\n", receipt.Refs["one"], receipt.Refs["two"])
		return
	}
	point := strings.TrimPrefix(role, "batch-crash-")
	err = store.lockedContext(context.Background(), func(root *os.Root) error {
		digest, err := requestRevision(context.Background(), request)
		if err != nil {
			return err
		}
		journal, err := prepareBatch(context.Background(), root, request, digest)
		if err != nil {
			return err
		}
		persistJournal(t, root, journal)
		if point != "journal" {
			for _, entry := range journal.Entries {
				if err := fixtureRootMkdir(root, pendingName(entry.Source), 0700); err != nil {
					return err
				}
				if err := syncNew(root, filepath.Join(pendingName(entry.Source), "batch"), []byte(filepath.Base(batchDirectory(request.Key)))); err != nil {
					return err
				}
				if err := syncPath(root, pendingName(entry.Source)); err != nil {
					return err
				}
				if err := syncPath(root, filepath.Dir(entry.Source)); err != nil {
					return err
				}
			}
			for i, entry := range journal.Entries {
				if point == "partial" && i > 0 {
					break
				}
				temporary := filepath.Join(batchDirectory(request.Key), entry.ID+".fixture")
				if err := syncNew(root, temporary, entry.After); err != nil {
					return err
				}
				if err := root.Link(temporary, entry.Source); err != nil {
					return err
				}
				if point != "published" {
					if err := syncPath(root, filepath.Dir(entry.Source)); err != nil {
						return err
					}
				}
			}
		}
		fmt.Println("ready")
		_, err = io.Copy(io.Discard, os.Stdin)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBatchKilledProcessesRecoverAcceptedWork(t *testing.T) {
	for _, point := range []string{"journal", "partial", "published"} {
		t.Run(point, func(t *testing.T) {
			path := t.TempDir()
			if _, err := Open(path); err != nil {
				t.Fatal(err)
			}
			command := helperProcess(t, "batch-crash-"+point, path)
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
				t.Fatalf("crash point unavailable: %q %v", line, err)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			result, err := reopened.ApplyBatch(context.Background(), batchProcessRequest())
			if err != nil || result.Status != "committed" {
				t.Fatalf("recovery: %+v %v", result, err)
			}
			for ref, want := range map[string]string{"one": "One", "two": "Two"} {
				doc, err := reopened.Read(result.Refs[ref])
				if err != nil || doc.Body != want {
					t.Fatal("accepted content lost")
				}
			}
			catalogue, err := reopened.Scan(context.Background())
			if err != nil || len(catalogue.Records) != 2 {
				t.Fatal("recovery duplicated or lost records")
			}
		})
	}
}

func TestConcurrentBatchProcessesReuseOneDurableIdentity(t *testing.T) {
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 4)
	for range 4 {
		command := helperProcess(t, "batch-retry", path)
		go func() {
			output, err := command.CombinedOutput()
			if err != nil {
				results <- "error:" + err.Error() + string(output)
				return
			}
			results <- string(output)
		}()
	}
	identity := ""
	for range 4 {
		output := <-results
		first := strings.SplitN(output, "\n", 2)[0]
		if !strings.HasPrefix(first, "identity:") {
			t.Fatalf("process failed: %s", output)
		}
		if identity != "" && identity != first {
			t.Fatalf("same request acquired different IDs: %s / %s", identity, first)
		}
		identity = first
	}
	c, err := store.Scan(context.Background())
	if err != nil || len(c.Records) != 2 {
		t.Fatal("concurrent retries duplicated records")
	}
}
