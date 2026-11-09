import { newID } from "./ids.js";

export const fileURL = (id, filename, preview = false) =>
  `/api/records/${encodeURIComponent(id)}/files/${encodeURIComponent(filename)}${preview ? "?preview=1" : ""}`;

export function relativeFileURL(id, value, preview = false) {
  if (!id || !value || /^(?:[a-z][a-z\d+.-]*:|\/|#)/i.test(value)) return value;
  try {
    const name = decodeURIComponent(value.replace(/^\.\//, ""));
    if (!name || /[/\\]/.test(name)) return value;
    return fileURL(id, name, preview);
  } catch {
    return value;
  }
}
const saved = {
  get(key) {
    try {
      return JSON.parse(localStorage.getItem(key) || "null");
    } catch {
      return null;
    }
  },
  set(key, value) {
    try {
      if (value === null) localStorage.removeItem(key);
      else localStorage.setItem(key, JSON.stringify(value));
    } catch {
      /* The server retains accepted uploads and operations. */
    }
  },
};
export async function uploadFile(request, file, scope, key = newID()) {
  return request(
    `/api/uploads/${key}?${new URLSearchParams({ scope, filename: file.name })}`,
    {
      method: "PUT",
      headers: { "Content-Type": "application/octet-stream" },
      body: file,
    },
  );
}
export function recordFiles(request, id) {
  return {
    files: [],
    fileText: {},
    fileError: "",
    fileURL,
    async init() {
      try {
        const result = await request(
          `/api/records/${encodeURIComponent(id)}/files`,
        );
        this.files = result.files;
        this.fileError = (result.diagnostics || [])
          .map((d) => d.message)
          .join(" ");
      } catch (e) {
        this.fileError = e.message;
      }
    },
    async previewText(file) {
      try {
        const value = await request(fileURL(id, file.filename) + "/text");
        this.fileText[file.filename] =
          value.text +
          (value.next ? "\n… Download the file to read the rest." : "");
      } catch (e) {
        this.fileError = e.message;
      }
    },
  };
}
export function filesUI(request) {
  let generation = 0;
  return {
    activePhoto: null,
    photoError: "",
    showPhoto(file) {
      this.activePhoto = { ...file, record: file.record || this.selected.id };
      this.photoError = "";
      this.$refs.photoDialog.showModal();
    },
    closePhoto() {
      this.$refs.photoDialog.close();
      this.activePhoto = null;
    },
    recordFiles: [],
    editorUploads: [],
    filesBusy: false,
    filesError: "",
    fileText: {},
    fileURL,
    editorFileScope() {
      const id = this.selected?.id;
      const key = id ? "songya-editor-scope:" + id : "songya-editor-new-scope";
      let scope = saved.get(key);
      if (!scope) {
        scope = id ? "editor:" + id : "editor:new:" + newID();
        saved.set(key, scope);
      }
      return scope;
    },
    bindEditorUploads(previousID, id) {
      if (previousID || !id) return;
      saved.set("songya-editor-scope:" + id, this.editorFileScope());
      saved.set("songya-editor-new-scope", null);
    },
    async loadEditorFiles() {
      const ticket = ++generation,
        id = this.selected?.id;
      this.recordFiles = [];
      this.editorUploads = [];
      this.fileText = {};
      this.filesError = "";
      this.profilePictures = [];
      this.profilePicturesError = "";
      try {
        const [listing, uploads, portraits] = await Promise.all([
          id
            ? request(`/api/records/${id}/files`)
            : Promise.resolve({ files: [] }),
          request(
            "/api/uploads?" +
              new URLSearchParams({ scope: this.editorFileScope() }),
          ),
          id && this.selected?.kind === "entities"
            ? request(`/api/records/${id}/profile-pictures`).catch((error) => ({
                pictures: [],
                diagnostics: [error.message],
              }))
            : Promise.resolve({ pictures: [], diagnostics: [] }),
          // A newly saved draft may still have completed uploads in the new-record scope.
        ]);
        if (ticket !== generation) return;
        this.recordFiles = listing.files;
        this.editorUploads = uploads.uploads;
        this.profilePictures = portraits.pictures;
        this.profilePicturesError = (portraits.diagnostics || []).join(" ");
        this.filesError = (listing.diagnostics || [])
          .map((d) => d.message)
          .join(" ");
      } catch (e) {
        if (ticket === generation) this.filesError = e.message;
      }
    },
    async uploadEditorFiles(files) {
      if (this.filesBusy || !this.selected) return;
      const id = this.selected.id,
        scope = this.editorFileScope();
      this.filesBusy = true;
      this.filesError = "";
      try {
        for (const file of files) {
          try {
            const receipt = await uploadFile(request, file, scope);
            if (this.selected?.id === id) this.editorUploads.push(receipt);
          } catch (e) {
            this.filesError += `${file.name}: ${e.message} `;
          }
        }
      } finally {
        this.filesBusy = false;
      }
      if (this.selected?.id === id && id && !this.dirty)
        await this.attachEditorUploads();
    },
    async changeRecordFile(upload, action = "add", filename = "") {
      const id = this.selected.id,
        key = `songya-file-attempt:${id}:${upload?.upload || action + ":" + filename}`;
      const attempt = saved.get(key) || {
        key: newID(),
        id,
        revision: null,
        action,
        ...(upload ? { upload: upload.upload } : {}),
        ...(filename ? { filename } : {}),
      };
      saved.set(key, attempt);
      const listing = await request(`/api/records/${id}/files`);
      if (attempt.revision === null) attempt.revision = listing.revision;
      saved.set(key, attempt);
      try {
        const receipt = await request(`/api/records/${id}/files`, {
          method: "POST",
          body: JSON.stringify(attempt),
        });
        saved.set(key, null);
        return receipt;
      } catch (e) {
        if (e.receipt?.status === "unavailable")
          saved.set(key, { ...attempt, revision: null });
        throw e;
      }
    },
    async attachEditorUploads() {
      if (
        this.filesBusy ||
        this.dirty ||
        !this.selected?.id ||
        !this.editorUploads.length
      )
        return;
      this.filesBusy = true;
      this.filesError = "";
      try {
        for (const upload of [...this.editorUploads]) {
          try {
            const receipt = await this.changeRecordFile(upload);
            this.editorUploads = this.editorUploads.filter(
              (item) => item.upload !== upload.upload,
            );
            this.notice = `Attached ${receipt.filename}`;
          } catch (e) {
            this.filesError += `${upload.filename}: ${e.message} `;
          }
        }
        // Refresh automatic fields without replacing a newer text draft.
        await this.refreshAfterFileChange();
      } finally {
        this.filesBusy = false;
      }
    },
    async removeRecordFile(file) {
      if (
        this.dirty ||
        this.filesBusy ||
        !confirm(
          `Permanently remove “${file.filename}”? The record's words will remain.`,
        )
      )
        return;
      this.filesBusy = true;
      this.filesError = "";
      try {
        await this.changeRecordFile(null, "remove", file.filename);
        await this.refreshAfterFileChange();
      } catch (e) {
        this.filesError = e.message;
      } finally {
        this.filesBusy = false;
      }
    },
    async replaceRecordFile(file, replacement) {
      if (
        !replacement ||
        this.dirty ||
        this.filesBusy ||
        !confirm(`Permanently replace the bytes of “${file.filename}”?`)
      )
        return;
      this.filesBusy = true;
      this.filesError = "";
      try {
        const upload = await uploadFile(
          request,
          replacement,
          this.editorFileScope(),
        );
        this.editorUploads.push(upload);
        await this.changeRecordFile(upload, "replace", file.filename);
        this.editorUploads = this.editorUploads.filter(
          (item) => item.upload !== upload.upload,
        );
        await this.refreshAfterFileChange();
        if (
          this.activePhoto?.record === this.selected?.id &&
          this.activePhoto.filename === file.filename
        )
          this.activePhoto = {
            ...this.activePhoto,
            ...this.recordFiles.find((item) => item.filename === file.filename),
          };
      } catch (e) {
        this.filesError = e.message;
      } finally {
        this.filesBusy = false;
      }
    },
    async previewFileText(file) {
      try {
        const value = await request(
          fileURL(this.selected.id, file.filename) + "/text",
        );
        this.fileText[file.filename] =
          value.text +
          (value.next ? "\n… Download the file to read the rest." : "");
      } catch (e) {
        this.filesError = e.message;
      }
    },
  };
}
