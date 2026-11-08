package songya

// The converter is deliberately separate from ordinary reads and writes. After
// cutover the engine has exactly one live layout, even when receipts describe
// an earlier location. It does not parse or serialize record contents.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LayoutFile struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Modified int64  `json:"modified_ns"`
	Mode     uint32 `json:"mode"`
}
type LayoutDirectory struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

type LayoutReport struct {
	PendingDirectories []LayoutDirectory `json:"pending_directories,omitempty"`
	Files              []LayoutFile      `json:"files"`
	Records            int               `json:"records"`
	Pending            int               `json:"pending"`
	History            int               `json:"history"`
}

const layoutInventory = ".layout-folders/inventory.json"

// ConvertFolders is an explicit offline operation. Stop ALL old-layout writers
// first. The immutable inventory and per-file synced progress survive a crash.
// A rerun verifies previously moved bytes before continuing. Unknown material is
// left alone; conflicting targets are never overwritten.
func (s *Store) ConvertFolders(ctx context.Context) (LayoutReport, error) {
	var report LayoutReport
	err := s.lockedContext(ctx, func(root *os.Root) error {
		if err := ensureDirectory(root, ".layout-folders"); err != nil {
			return err
		}
		if data, err := readFile(root, layoutInventory); err == nil {
			if err := json.Unmarshal(data, &report); err != nil {
				return fmt.Errorf("conversion inventory retained but unreadable: %w", err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		} else {
			var err error
			report, err = inventoryLayout(ctx, root)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			if err := publishImmutable(root, layoutInventory, data); err != nil {
				return err
			}
		}
		for _, directory := range report.PendingDirectories {
			if err := validateLayoutTarget(LayoutFile{Source: filepath.Join(directory.Source, "retained"), Target: filepath.Join(directory.Target, "retained"), SHA256: strings.Repeat("0", 64)}); err != nil {
				return err
			}
			parts := strings.Split(directory.Target, string(filepath.Separator))
			for i := range parts {
				if err := ensureDirectory(root, filepath.Join(parts[:i+1]...)); err != nil {
					return err
				}
			}
		}

		for _, file := range report.Files {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := convertLayoutFile(root, file); err != nil {
				return fmt.Errorf("%s retained; conversion can resume: %w", file.Source, err)
			}
			progress := filepath.Join(".layout-folders", dataRevision([]byte(file.Source))+".done")
			if err := mark(root, progress); err != nil {
				return err
			}
		}
		// Deepest first: remove ONLY emptied old directories. Unknown work stays.
		for i := len(report.PendingDirectories) - 1; i >= 0; i-- {
			directory := report.PendingDirectories[i]
			if err := root.Remove(directory.Source); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := syncExistingParent(root, filepath.Dir(directory.Source)); err != nil {
				return err
			}
		}

		return mark(root, ".layout-folders/complete")
	})
	return report, err
}

func inventoryLayout(ctx context.Context, root *os.Root) (LayoutReport, error) {
	report := LayoutReport{Files: []LayoutFile{}}
	add := func(source, target string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := readFile(root, source)
		if err != nil {
			return err
		}
		info, err := root.Lstat(source)
		if err != nil {
			return err
		}
		report.Files = append(report.Files, LayoutFile{source, target, dataRevision(data), info.Size(), info.ModTime().UnixNano(), uint32(info.Mode().Perm())})
		return nil
	}
	for _, kind := range destinations {
		entries, err := fs.ReadDir(root.FS(), string(kind))
		if err != nil {
			return report, err
		}
		for _, entry := range entries {
			id := strings.TrimSuffix(entry.Name(), ".md")
			if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && strings.HasSuffix(entry.Name(), ".md") {
				if id == "" {
					return report, fmt.Errorf("ambiguous flat name %s retained; repair its filename before conversion", entry.Name())
				}
				target := filepath.Join(string(kind), id, "record.md")
				if err := add(filepath.Join(string(kind), entry.Name()), target); err != nil {
					return report, err
				}
				report.Records++
			}
			id = strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "."), ".md.pending")
			if entry.Name() != "."+id+".md.pending" || !ValidID(id) || kindOf(id) != kind {
				continue
			}
			source := filepath.Join(string(kind), entry.Name())
			target, _ := recordName(id)
			err := fs.WalkDir(root.FS(), source, func(path string, e fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if e.IsDir() {
					relative, err := filepath.Rel(source, path)
					if err != nil {
						return err
					}
					report.PendingDirectories = append(report.PendingDirectories, LayoutDirectory{path, filepath.Join(pendingName(target), relative)})
					return nil
				}
				relative, err := filepath.Rel(source, path)
				if err != nil {
					return err
				}
				report.Pending++
				return add(path, filepath.Join(pendingName(target), relative))
			})
			if err != nil {
				return report, err
			}
		}
	}
	// Preserve checksum-protected history by proving it unchanged, not rewriting it.
	for _, directory := range []string{".batches", ".imports"} {
		err := fs.WalkDir(root.FS(), directory, func(path string, e fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == directory {
				return nil
			}
			if err != nil {
				return err
			}
			if e.IsDir() {
				return nil
			}
			report.History++
			return add(path, path)
		})
		if err != nil {
			return report, err
		}
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].Source < report.Files[j].Source })
	return report, nil
}

func verifyLayoutFile(root *os.Root, name string, file LayoutFile) error {
	data, err := readFile(root, name)
	if err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if dataRevision(data) != file.SHA256 || info.Size() != file.Bytes || info.ModTime().UnixNano() != file.Modified || uint32(info.Mode().Perm()) != file.Mode {
		return fmt.Errorf("bytes or filesystem metadata differ from inventory at %s", name)
	}
	return nil
}
func convertLayoutFile(root *os.Root, file LayoutFile) error {
	if err := validateLayoutTarget(file); err != nil {
		return err
	}
	if file.Source == file.Target {
		return verifyLayoutFile(root, file.Target, file)
	}
	if _, err := root.Lstat(file.Target); err == nil {
		if err := verifyLayoutFile(root, file.Target, file); err != nil {
			return err
		}
		if _, err := root.Lstat(file.Source); err == nil {
			return fmt.Errorf("both old and new names exist; neither was removed")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else {
		if err := verifyLayoutFile(root, file.Source, file); err != nil {
			return err
		}
		// Pending paths can have retained subdirectories. Create each parent safely.
		parent := filepath.Dir(file.Target)
		parts := strings.Split(parent, string(filepath.Separator))
		for i := range parts {
			if err := ensureDirectory(root, filepath.Join(parts[:i+1]...)); err != nil {
				return err
			}
		}
		if err := root.Rename(file.Source, file.Target); err != nil {
			return err
		}
	}
	// Re-establish directory durability even on a retry after rename but before sync.
	if err := syncPath(root, file.Target); err != nil {
		return err
	}
	if err := syncPath(root, filepath.Dir(file.Target)); err != nil {
		return err
	}
	if err := syncExistingParent(root, filepath.Dir(file.Source)); err != nil {
		return err
	}
	return verifyLayoutFile(root, file.Target, file)
}

func syncExistingParent(root *os.Root, directory string) error {
	for {
		err := syncPath(root, directory)
		if !errors.Is(err, fs.ErrNotExist) || directory == "." {
			return err
		}
		directory = filepath.Dir(directory)
	}
}

func validateLayoutTarget(file LayoutFile) error {
	source := filepath.ToSlash(file.Source)
	target := filepath.ToSlash(file.Target)
	if filepath.ToSlash(filepath.Clean(source)) != source || filepath.ToSlash(filepath.Clean(target)) != target || !validHash(file.SHA256) {
		return errors.New("invalid conversion receipt; retained")
	}
	parts := strings.Split(source, "/")
	if file.Source == file.Target {
		if len(parts) < 2 || (parts[0] != ".batches" && parts[0] != ".imports") {
			return errors.New("unexpected historical source in inventory")
		}
		return nil
	}
	if len(parts) < 2 {
		return errors.New("invalid conversion source")
	}
	if _, err := Destination(parts[0]).prefix(); err != nil {
		return err
	}
	if len(parts) == 2 && !strings.HasPrefix(parts[1], ".") && strings.HasSuffix(parts[1], ".md") {
		if target == filepath.ToSlash(filepath.Join(parts[0], strings.TrimSuffix(parts[1], ".md"), "record.md")) {
			return nil
		}
	}
	id := strings.TrimSuffix(strings.TrimPrefix(parts[1], "."), ".md.pending")
	if ValidID(id) && parts[1] == "."+id+".md.pending" && kindOf(id) == Destination(parts[0]) {
		name, _ := recordName(id)
		if target == filepath.ToSlash(filepath.Join(append([]string{pendingName(name)}, parts[2:]...)...)) {
			return nil
		}
	}
	return errors.New("conversion target does not belong to its original source; retained")
}
