import Alpine from "alpinejs";
import { codeEditor, richEditor } from "./editors.js";
import { memoryGraph } from "./graph.js";
import { chatUI } from "./chat.js";
import { recallUI } from "./recall.js";
import { defaultKindColors } from "./palette.js";
import { typesUI } from "./types.js";
import { newID } from "./ids.js";
import { storyUI } from "./story.js";

async function request(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
  });
  const value = await response.json();
  if (!response.ok)
    throw Object.assign(new Error(value.error || "Request failed"), value, {
      status: response.status,
    });
  return value;
}
Alpine.data("songya", () => {
  let code,
    rich,
    graph,
    graphData,
    graphRecords = [],
    editGeneration = 0,
    listing = 0,
    graphListing = 0,
    attempt = null,
    base = "",
    body = "",
    metadataStamp = "";
  let listingFailure = "";
  const drafts = new Map();
  return {
    ...chatUI(request),
    ...recallUI(request),
    ...typesUI(request),
    ...storyUI(request),
    theme: document.documentElement.dataset.theme || "system",
    setTheme() {
      document.documentElement.dataset.theme = this.theme;
      graph?.restyle();
      try {
        localStorage.setItem("songya-theme", this.theme);
      } catch {
        /* Preference is optional. */
      }
    },
    records: [],
    owners: {},
    types: {},
    filters: { title: "", description: "", type: "", aliases: "" },
    sortKey: "updated_at",
    ascending: false,
    dialogOpen: false,
    inspecting: false,
    q: "",
    kind: "",
    owner: "",
    selected: null,
    source: "",
    fields: [],
    mode: "visual",
    dirty: false,
    busy: false,
    notice: "",
    failure: "",
    diagnostics: [],
    links: [],
    blockers: [],
    showMetadata: false,
    showConnections: false,
    targetQuery: "",
    targets: [],
    target: null,
    relation: "related_to",
    visualIssue: "",
    recordIssues: [],
    loading: false,
    conflict: false,
    graphStatus: "Loading graph…",
    graphQuery: "",
    graphMatches: [],
    graphSearchOpen: false,
    graphMode: "2d",
    graphExpanded: false,
    graphOwner: "",
    graphOwners: [],
    graphKindColors: { ...defaultKindColors },
    async init() {
      this.$watch("recallDraft", () => this.keepRecallDraft());
      this.refreshGraph();
      await this.refresh();
      await this.initChat();
      await this.initTypes();
      window.addEventListener("beforeunload", (e) => {
        if (
          this.dirty ||
          attempt ||
          [...drafts.values()].some((d) => d.dirty || d.attempt)
        ) {
          e.preventDefault();
          e.returnValue = "";
        }
      });
    },
    async refreshGraph() {
      const ticket = ++graphListing;
      try {
        const data = await request(
          "/api/graph?" +
            new URLSearchParams({
              classification_user: this.classificationUser,
            }),
        );
        if (ticket !== graphListing) return;
        graphData = data;
        this.graphKindColors = data.kind_colors || { ...defaultKindColors };
        this.graphOwners = [
          ...new Set([
            ...data.nodes.map((node) => node.user).filter(Boolean),
            ...(this.graphOwner ? [this.graphOwner] : []),
          ]),
        ].sort();
        this.renderGraph();
        return true;
      } catch {
        if (ticket === graphListing)
          this.graphStatus =
            "Graph unavailable. You can still browse the library.";
        return false;
      }
    },
    renderGraph() {
      if (!graphData) return;
      let nodes = graphData.nodes;
      if (this.graphOwner) {
        const owned = new Set(
          nodes
            .filter(
              (node) =>
                node.user === this.graphOwner &&
                ["memories", "stories"].includes(node.kind),
            )
            .map((node) => node.id),
        );
        const shared = new Set(
          nodes
            .filter((node) => ["entities", "topics"].includes(node.kind))
            .map((node) => node.id),
        );
        const visible = new Set(owned);
        for (const link of graphData.links) {
          if (owned.has(link.source) && shared.has(link.target))
            visible.add(link.target);
          if (owned.has(link.target) && shared.has(link.source))
            visible.add(link.source);
        }
        nodes = nodes.filter((node) => visible.has(node.id));
      }
      const ids = new Set(nodes.map((node) => node.id));
      const links = graphData.links.filter(
        (link) => ids.has(link.source) && ids.has(link.target),
      );
      graphRecords = nodes;
      try {
        graph ||= memoryGraph(
          this.$refs.graph,
          (id) => this.open(id, false, true),
          this.graphMode,
          (node) => node.color || this.graphKindColors[node.kind],
        );
        graph.update({ nodes, links });
        if (this.graphQuery) this.searchGraph();
        this.graphStatus = nodes.length
          ? ""
          : this.graphOwner
            ? "No records for this owner."
            : "No records yet.";
      } catch {
        this.graphStatus =
          "Graph unavailable. You can still browse the library.";
      }
    },
    switchGraph(mode) {
      if (this.graphMode === mode && graph) return;
      graph?.destroy();
      graph = null;
      this.$refs.graph.replaceChildren();
      this.graphMode = mode;
      this.renderGraph();
    },
    toggleGraphExpanded() {
      this.graphExpanded = !this.graphExpanded;
      this.$nextTick(() => this.$refs.graphExpand.focus());
    },
    searchGraph() {
      const query = this.graphQuery.trim().toLocaleLowerCase();
      this.graphSearchOpen = !!query;
      this.graphMatches = query
        ? graphRecords
            .filter((node) =>
              [node.title, node.id, ...(node.aliases || [])].some((text) =>
                (text || "").toLocaleLowerCase().includes(query),
              ),
            )
            .sort((a, b) => (a.title || a.id).localeCompare(b.title || b.id))
        : [];
      graph?.highlight(query ? this.graphMatches.map((node) => node.id) : null);
      if (!query) graph?.focus();
    },
    focusGraph(id) {
      if (id) {
        graph?.focus(id);
        this.graphSearchOpen = false;
      }
    },
    async refresh() {
      const ticket = ++listing;
      try {
        const result = await request(
          "/api/records?" +
            new URLSearchParams({
              q: this.q,
              kind: this.kind,
              user: this.owner,
              classification_user: this.classificationUser,
            }),
        );
        if (ticket !== listing) return;
        this.records = result.records;
        this.owners = result.owners;
        this.types = result.types || {};
        this.diagnostics = result.diagnostics || [];
        if (listingFailure && this.failure === listingFailure)
          this.failure = "";
        listingFailure = "";
        return true;
      } catch (e) {
        if (ticket === listing) this.failure = listingFailure = e.message;
        return false;
      }
    },
    rows() {
      const text = (record, key) =>
        key === "aliases"
          ? (record.aliases || []).join(", ")
          : key === "user"
            ? record.user || "Shared"
            : record[key] || "";
      return this.records
        .filter((record) =>
          Object.entries(this.filters).every(([key, value]) =>
            key === "type"
              ? !value || text(record, key) === value
              : text(record, key)
                  .toLocaleLowerCase()
                  .includes(value.toLocaleLowerCase()),
          ),
        )
        .sort((a, b) => {
          if (this.sortKey === "updated_at") {
            const left = Date.parse(a.updated_at),
              right = Date.parse(b.updated_at);
            if (!Number.isFinite(left) || !Number.isFinite(right))
              return Number.isFinite(left)
                ? -1
                : Number.isFinite(right)
                  ? 1
                  : a.id.localeCompare(b.id);
            return (
              (this.ascending ? left - right : right - left) ||
              a.id.localeCompare(b.id)
            );
          }
          return (
            (this.ascending ? 1 : -1) *
            (text(a, this.sortKey).localeCompare(
              text(b, this.sortKey),
              undefined,
              { numeric: true, sensitivity: "base" },
            ) || a.id.localeCompare(b.id))
          );
        });
    },
    sort(key) {
      this.ascending =
        this.sortKey === key ? !this.ascending : key !== "updated_at";
      this.sortKey = key;
    },
    updatedLabel(value) {
      return Number.isFinite(Date.parse(value))
        ? new Date(value).toLocaleString()
        : "Unknown";
    },
    sortOrder(key) {
      return this.sortKey === key
        ? this.ascending
          ? "ascending"
          : "descending"
        : "none";
    },
    showDialog() {
      this.dialogOpen = true;
      if (!this.$refs.editorDialog.open) this.$refs.editorDialog.showModal();
    },
    closeDialog() {
      this.resetStory();
      this.$refs.editorDialog.close();
      this.dialogOpen = false;
      code?.destroy();
      rich?.destroy();
      code = null;
      rich = null;
      this.selected = null;
      this.source = "";
      this.dirty = false;
      attempt = null;
    },
    edit() {
      this.resetStory();
      this.inspecting = false;
      this.mount();
    },
    remember() {
      if (!this.selected) return;
      if (!this.dirty && !attempt) {
        drafts.delete(this.selected.id || "new");
        return;
      }
      drafts.set(this.selected.id || "new", {
        selected: { ...this.selected },
        source: this.source,
        base,
        attempt,
        dirty: this.dirty,
        fields: structuredClone(Alpine.raw(this.fields)),
        body,
        links: structuredClone(Alpine.raw(this.links)),
        issues: this.recordIssues,
        mode: this.mode,
        stamp: metadataStamp,
      });
    },
    async stash() {
      try {
        await this.flush();
      } catch (e) {
        this.notice = "Draft kept in this tab. " + e.message;
      }
      this.remember();
    },
    async open(id, force = false, inspect = false) {
      if (this.busy || this.loading) return;
      this.loading = true;
      this.failure = "";
      this.notice = "";
      try {
        if (!force) await this.stash();
        else this.remember();
        let saved = force ? null : drafts.get(id);
        if (!saved || (!saved.dirty && !saved.attempt)) {
          const record = await request(
            "/api/records/" + encodeURIComponent(id),
          );
          saved = {
            selected: {
              id: record.id,
              kind: record.kind,
              version: record.version,
              pending: record.pending,
            },
            source: record.source,
            base: record.source,
            fields: record.draft.fields,
            body: record.draft.body,
            links: record.links,
            issues: record.diagnostics,
            mode: "visual",
            dirty: false,
            attempt: null,
          };
        }
        this.inspecting = inspect;
        if (inspect && saved.selected.kind === "stories")
          saved = { ...saved, mode: "visual" };
        if (!this.classificationUser)
          this.classificationUser =
            this.chatUser || this.classificationUsers()[0] || "";
        if (this.classificationUser) await this.loadClassificationPolicy();
        this.install(saved);
        if (force) {
          drafts.delete(id);
        }
        this.blockers = [];
        this.showDialog();
      } catch (e) {
        this.failure = e.message;
      } finally {
        this.loading = false;
      }
    },
    install(saved) {
      this.resetStory();
      this.conflict = false;
      this.recordIssues = saved.issues || [];
      this.selected = saved.selected;
      this.source = saved.source;
      base = saved.base;
      attempt = saved.attempt;
      body = saved.body;
      this.fields = saved.fields;
      metadataStamp = saved.stamp || JSON.stringify(this.fields);
      this.links = saved.links || [];
      this.mode = saved.mode;
      this.dirty = saved.dirty;
      this.mount();
    },
    async create() {
      if (this.busy || this.loading) return;
      this.loading = true;
      try {
        await this.stash();
        this.inspecting = false;
        const saved = drafts.get("new");
        if (saved?.dirty) {
          this.install(saved);
        } else {
          const result = await request("/api/draft", {
            method: "POST",
            body: JSON.stringify({
              source:
                '---\ntitle: ""\nuser: ""\ntime: {date: null, earliest: null, latest: null}\n---\n',
            }),
          });
          this.install({
            selected: { id: "", kind: "memories", version: "" },
            source: result.source,
            base: "",
            fields: result.draft.fields,
            body: result.draft.body,
            links: [],
            mode: "visual",
            dirty: true,
            attempt: null,
          });
        }
        if (!this.classificationUser)
          this.classificationUser =
            this.chatUser || this.classificationUsers()[0] || "";
        if (this.classificationUser) await this.loadClassificationPolicy();
        this.showDialog();
        this.showMetadata = true;
        this.failure = "";
        this.notice = "";
      } catch (e) {
        this.failure = e.message;
      } finally {
        this.loading = false;
      }
    },
    mount() {
      code?.destroy();
      rich?.destroy();
      code = null;
      rich = null;
      this.visualIssue = "";
      this.$nextTick(() => {
        if (!this.selected) return;
        if (this.readingStory() && this.mode === "visual") {
          if (!this.storyError) this.loadStory();
          return;
        }
        if (this.mode === "source") {
          code = codeEditor(
            this.$refs.source.firstElementChild,
            this.source,
            (text) => {
              this.source = text;
              this.dirty = text !== base || !!attempt;
              editGeneration++;
            },
            this.fields
              .filter((field) => field.system)
              .map((field) => field.protected),
            this.inspecting,
          );
        } else {
          try {
            rich = richEditor(
              this.$refs.visual.firstElementChild,
              body,
              (text) => {
                body = text;
                this.dirty = true;
                editGeneration++;
              },
              this.inspecting,
            );
          } catch {
            this.visualIssue =
              "This Markdown needs the Source editor. Your text is preserved.";
          }
        }
      });
    },
    async flush() {
      if (!this.selected) return;
      if (this.mode === "source") {
        const value = await request("/api/draft", {
          method: "POST",
          body: JSON.stringify({
            source: this.source,
            id: this.selected.id,
            kind: this.selected.kind,
          }),
        });
        body = value.draft.body;
        this.fields = value.draft.fields;
        metadataStamp = JSON.stringify(this.fields);
        this.links = value.links || this.links;
        return;
      }
      const changedFields = JSON.stringify(this.fields) !== metadataStamp;
      const result = await request("/api/draft", {
        method: "POST",
        body: JSON.stringify({
          source: this.source,
          id: this.selected.id,
          kind: this.selected.kind,
          ...(this.dirty ? { body } : {}),
          ...(changedFields ? { fields: this.fields } : {}),
        }),
      });
      this.source = result.source;
      body = result.draft.body;
      this.fields = result.draft.fields;
      metadataStamp = JSON.stringify(this.fields);
      this.dirty = this.source !== base || !!attempt;
      this.links = result.links || this.links;
    },
    async changeRecordType(value) {
      if (
        this.busy ||
        this.loading ||
        !this.selected ||
        !this.classificationUser
      )
        return;
      this.loading = true;
      this.failure = "";
      try {
        await this.flush();
        const result = await request("/api/draft", {
          method: "POST",
          body: JSON.stringify({
            source: this.source,
            id: this.selected.id,
            kind: this.selected.kind,
            classification_user: this.classificationUser,
            classification_type: value,
          }),
        });
        this.source = result.source;
        this.fields = result.draft.fields;
        body = result.draft.body;
        metadataStamp = JSON.stringify(this.fields);
        this.dirty = this.source !== base || !!attempt;
        editGeneration++;
        this.remember();
      } catch (e) {
        this.failure = e.message;
      } finally {
        this.loading = false;
      }
    },
    async switchMode(mode) {
      if (mode === this.mode || this.busy || this.loading) return;
      this.loading = true;
      try {
        await this.flush();
        this.mode = mode;
        this.mount();
      } catch (e) {
        this.failure = e.message;
      } finally {
        this.loading = false;
      }
    },
    async panel(name) {
      if (this.mode === "source") {
        await this.switchMode("visual");
        if (this.mode !== "visual") return;
        this[name] = true;
      } else this[name] = !this[name];
      if (name === "showConnections") await this.searchTargets();
    },
    changeFieldType(field) {
      if (field.type === "boolean")
        field.text = field.text === "true" ? "true" : "false";
      if (field.type !== "yaml") this.fieldChanged(field);
      else this.metadataChanged();
    },
    setKind(kind) {
      this.selected.kind = kind;
      const timing = this.fields.find((field) => field.key === "time");
      if (timing?.type === "time") {
        if (Object.values(timing.time).every((value) => !value)) {
          timing.time =
            kind === "entities"
              ? { start: null, end: null }
              : { date: null, earliest: null, latest: null };
          timing.value = JSON.stringify(timing.time);
        } else if (
          (kind === "entities") !==
          Object.hasOwn(timing.time, "start")
        ) {
          timing.type = "yaml";
          this.failure =
            "The existing time values are retained. Entities use start/end; other records use date/earliest/latest. Choose the intended dates before saving.";
        }
      }
      const owned = kind === "memories" || kind === "stories";
      if (!owned) this.fields = this.fields.filter((f) => f.key !== "user");
      else if (!this.fields.some((f) => f.key === "user"))
        this.fields.push({
          index: -1,
          key: "user",
          value: '""',
          text: "",
          type: "text",
          system: false,
        });
      if (!owned && !this.fields.some((f) => f.key === "name"))
        this.fields.unshift({
          index: -1,
          key: "name",
          value: '""',
          text: "",
          type: "text",
          system: false,
        });
      this.metadataChanged();
    },
    addLifetimeDates() {
      this.fields.push({
        index: -1,
        key: "time",
        type: "time",
        time: { start: null, end: null },
        value: '{"start":null,"end":null}',
        text: "",
        system: false,
      });
      this.metadataChanged();
    },
    timeChanged(field, exact) {
      const lifetime = this.selected?.kind === "entities";
      if (!lifetime) {
        if (exact) field.time.earliest = field.time.latest = null;
        else field.time.date = null;
      }
      for (const key of lifetime
        ? ["start", "end"]
        : ["date", "earliest", "latest"])
        field.time[key] ||= null;
      field.value = JSON.stringify(field.time);
      this.metadataChanged();
    },
    async moveConnection(edge, direction) {
      await this.connection(direction < 0 ? "up" : "down", edge);
    },
    fieldChanged(field) {
      field.value =
        field.type === "text" ? JSON.stringify(field.text) : field.text;
      this.metadataChanged();
    },
    metadataChanged() {
      this.dirty = true;
    },
    addField() {
      this.fields.push({
        index: -1,
        key: "",
        value: '""',
        type: "text",
        text: "",
        system: false,
      });
      this.metadataChanged();
    },
    removeField(index) {
      this.fields.splice(index, 1);
      this.metadataChanged();
    },
    async save() {
      if (this.busy || this.loading || !this.selected) return;
      this.busy = true;
      this.failure = "";
      this.notice = "";
      try {
        await this.flush();
        const current = this.source;
        attempt ||= {
          key: newID(),
          id: this.selected.id,
          kind: this.selected.kind,
          version: this.selected.version,
          source: current,
          classification_user: this.classificationUser,
        };
        const submitted = attempt;
        const generation = editGeneration;
        const result = await request("/api/records", {
          method: "POST",
          body: JSON.stringify(submitted),
        });
        this.selected = {
          id: result.id,
          kind: result.kind,
          version: result.version,
        };
        base = result.source;
        attempt = null;
        // Resolve the original attempt first; edits made after a lost reply remain a draft.
        if (current === submitted.source && generation === editGeneration) {
          this.source = result.source;
          body = result.draft.body;
          this.fields = result.draft.fields;
          metadataStamp = JSON.stringify(this.fields);
          this.dirty = false;
          this.mount();
        } else {
          this.dirty = true;
          this.notice =
            "Previous save confirmed. Your newer changes are still unsaved.";
        }
        drafts.delete("new");
        this.remember();
        if (!this.notice) this.notice = "Saved";
        this.refreshGraph();
        await this.refresh();
        const record = await request("/api/records/" + result.id);
        this.links = record.links;
      } catch (e) {
        if (e.rejected) attempt = null;
        this.failure = e.message;
        this.conflict = e.status === 409;
      } finally {
        this.busy = false;
      }
    },
    async remove(record = this.selected) {
      if (this.busy || this.loading || !record?.id) return;
      if (
        (this.selected?.id === record.id && this.dirty) ||
        drafts.get(record.id)?.dirty ||
        drafts.get(record.id)?.attempt
      ) {
        this.failure =
          "Save or discard your draft before deleting this record.";
        return;
      }
      this.busy = true;
      this.failure = "";
      this.blockers = [];
      try {
        await request("/api/records/" + record.id, {
          method: "DELETE",
          headers: { "If-Match": record.version },
        });
        drafts.delete(record.id);
        if (this.selected?.id === record.id) this.closeDialog();
        this.notice = "Deleted permanently";
        this.refreshGraph();
        await this.refresh();
      } catch (e) {
        if (!this.dialogOpen && (e.blockers?.length || e.diagnostics?.length)) {
          this.busy = false;
          await this.open(record.id, false, true);
        }
        this.failure = e.message;
        this.blockers = e.blockers || [];
        this.diagnostics = e.diagnostics || [];
      } finally {
        this.busy = false;
      }
    },
    async searchTargets() {
      const query = this.targetQuery;
      try {
        const result = await request(
          "/api/records?" + new URLSearchParams({ q: query }),
        );
        if (query === this.targetQuery)
          this.targets = result.records.filter(
            (r) => r.id !== this.selected?.id && r.id,
          );
      } catch (e) {
        this.failure = e.message;
      }
    },
    async connection(action, edge) {
      if (this.busy || this.loading) return;
      this.loading = true;
      try {
        await this.flush();
        const result = await request("/api/draft", {
          method: "POST",
          body: JSON.stringify({
            source: this.source,
            id: this.selected.id,
            kind: this.selected.kind,
            action,
            type: edge.type,
            target: edge.target,
          }),
        });
        this.source = result.source;
        body = result.draft.body;
        this.fields = result.draft.fields;
        metadataStamp = JSON.stringify(this.fields);
        this.links = result.links;
        this.dirty = this.source !== base || !!attempt;
        this.mount();
        this.target = null;
        this.targets = [];
        this.targetQuery = "";
      } catch (e) {
        this.failure = e.message;
      } finally {
        this.loading = false;
      }
    },
    async addConnection() {
      if (this.target)
        await this.connection("add", {
          type: this.relation || "related_to",
          target: this.target.id,
        });
    },
    back() {
      if (this.busy || this.loading) return;
      this.remember();
      this.closeDialog();
    },
    label(record) {
      return record.title || record.id || "Untitled";
    },
    title() {
      for (const key of ["title", "name"]) {
        const f = this.fields.find((f) => f.key === key);
        if (!f) continue;
        let value = f.value;
        try {
          value = JSON.parse(value);
        } catch {
          value = value.trim();
        }
        if (value) return String(value);
      }
      return "Untitled";
    },
    async discard() {
      if (this.busy || this.loading) return;
      if (this.selected?.id) {
        await this.reload();
        return;
      }
      if (!confirm("Discard this unsaved draft?")) return;
      const pending = !!attempt;
      drafts.delete("new");
      attempt = null;
      this.closeDialog();
      this.failure = "";
      this.notice = pending
        ? "Local draft discarded. A save awaiting confirmation may already be in the library."
        : "Draft discarded";
      await this.refresh();
    },
    async reload() {
      if (
        this.selected?.id &&
        confirm(
          "Replace this draft with the saved record? Unsaved changes will be discarded.",
        )
      ) {
        await this.open(this.selected.id, true);
      }
    },

    outgoing() {
      return this.links.filter(
        (e) => e.source === this.selected?.id || e.source === "draft",
      );
    },
    incoming() {
      return this.links.filter(
        (e) => e.target === this.selected?.id && e.source !== this.selected?.id,
      );
    },
  };
});
window.Alpine = Alpine;
Alpine.start();
