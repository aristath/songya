import {
  colorPresets,
  mochaColor,
  colorCSS,
  isPreset,
  defaultKindColors,
} from "./palette.js";
import { newID } from "./ids.js";
const storage = {
  get(key) {
    try {
      return localStorage.getItem(key);
    } catch {
      return null;
    }
  },
  set(key, value) {
    try {
      if (value === null) localStorage.removeItem(key);
      else localStorage.setItem(key, value);
    } catch {
      /* The open form retains its draft. */
    }
  },
};
const copy = (value) => JSON.parse(JSON.stringify(value));
export function typesUI(request) {
  let generation = 0;
  return {
    colorPresets,
    mochaColor,
    colorCSS,
    colorMode(value) {
      return isPreset(value) ? value : "custom";
    },
    typeKindColors: null,
    typeMyEntity: "",
    typeEntityChoices() {
      const entities = (this.timelineSubjects || []).filter(
        (r) => r.kind === "entities",
      );
      if (
        this.typeMyEntity &&
        !entities.some((r) => r.id === this.typeMyEntity)
      )
        entities.push({ id: this.typeMyEntity, title: this.typeMyEntity });
      return entities;
    },
    setKindColor(kind, value) {
      this.typeKindColors[kind] =
        value === "custom" ? mochaColor(this.typeKindColors[kind]) : value;
      this.keepTypeDraft();
    },
    setTypeColor(type, value) {
      type.color = value === "custom" ? mochaColor(type.color) : value;
      this.keepTypeDraft();
    },
    classificationUser: storage.get("songya-classification-user") || "",
    classificationPolicy: null,
    classificationBusy: false,
    typeUsers: [],
    typeSettingsUser: "",
    typeSettings: null,
    typeDraft: null,
    typeReplacements: {},
    typePending: null,
    typeSettingsKind: "entities",
    typeSettingsBusy: false,
    typeSettingsError: "",
    typeSettingsNotice: "",
    typeKinds: ["entities", "memories", "topics", "stories"],
    async initTypes() {
      try {
        const { users } = await request("/api/type-users");
        this.typeUsers = users;
        this.classificationUser ||= this.chatUser || users[0] || "";
        if (this.classificationUser) {
          await this.loadClassificationPolicy();
          await Promise.all([this.refresh(), this.refreshGraph()]);
        }
      } catch (e) {
        this.failure = e.message;
      }
    },
    classificationUsers() {
      return [
        ...new Set([
          ...this.typeUsers,
          ...Object.keys(this.owners),
          ...this.chatUsers(),
          ...(this.classificationUser ? [this.classificationUser] : []),
        ]),
      ].sort();
    },
    async loadClassificationPolicy() {
      const ticket = ++generation;
      this.classificationBusy = true;
      storage.set("songya-classification-user", this.classificationUser);
      try {
        const value = await request(
          "/api/types/" + encodeURIComponent(this.classificationUser),
        );
        if (ticket === generation) this.classificationPolicy = value;
      } catch (e) {
        if (ticket === generation) this.failure = e.message;
      } finally {
        if (ticket === generation) this.classificationBusy = false;
      }
    },
    typeChoices(kind) {
      return this.classificationPolicy?.user === this.classificationUser
        ? this.classificationPolicy.kinds[kind] || []
        : [];
    },
    typeLabel(id, kind) {
      const lists = kind
        ? this.typeChoices(kind)
        : Object.values(this.classificationPolicy?.kinds || {}).flat();
      return lists.find((t) => t.id === id)?.name || id;
    },
    recordType() {
      return (
        this.fields.find((f) => f.key === "types")?.assignments?.[
          this.classificationUser
        ] || ""
      );
    },
    recordTypeDefinition() {
      return (
        this.typeChoices(this.selected?.kind).find(
          (t) => t.id === this.recordType(),
        )?.description || ""
      );
    },
    async changeClassificationUser() {
      await this.loadClassificationPolicy();
      this.filters.type = "";
      await Promise.all([this.refresh(), this.refreshGraph()]);
    },
    typeDraftKey(user = this.typeSettings?.user) {
      return "songya-type-draft:" + user;
    },
    keepTypeDraft() {
      if (!this.typeSettings || !this.typeDraft) return;
      storage.set(
        this.typeDraftKey(),
        JSON.stringify({
          version: this.typeSettings.version,
          kinds: this.typeDraft,
          kind_colors: this.typeKindColors,
          my_entity: this.typeMyEntity,
          replacements: this.typeReplacements,
          pending: this.typePending,
        }),
      );
    },
    async openTypeSettings(user) {
      this.typeSettingsUser =
        user ||
        this.chatUser ||
        this.classificationUser ||
        this.classificationUsers()[0] ||
        "";
      this.$refs.typeSettingsDialog.showModal();
      if (this.typeSettingsUser) await this.loadTypeSettings();
    },
    async loadTypeSettings(reload = false) {
      this.keepTypeDraft();
      this.typeSettings = this.typeDraft = null;
      this.typeSettingsBusy = true;
      this.typeSettingsError = this.typeSettingsNotice = "";
      try {
        const user = this.typeSettingsUser.trim();
        if (!user || user === "all")
          throw Error("Choose one user for type settings.");
        const value = await request("/api/types/" + encodeURIComponent(user));
        const raw = storage.get(this.typeDraftKey(user));
        let saved = null;
        if (raw && !reload) {
          try {
            saved = JSON.parse(raw);
            if (!saved?.kinds || typeof saved.version !== "string")
              throw Error();
          } catch {
            throw Error(
              "The saved type draft cannot be read. It is retained in this browser; reload saved types to recover.",
            );
          }
        }
        this.typeSettings = {
          ...value,
          version: saved?.version ?? value.version,
        };
        this.typeDraft = copy(saved?.kinds || value.kinds);
        this.typeKindColors = copy(saved?.kind_colors || value.kind_colors);
        this.typeMyEntity = saved?.my_entity ?? value.my_entity ?? "";
        for (const kind of this.typeKinds)
          for (const type of this.typeDraft[kind] || [])
            type.color ||=
              value.kinds[kind]?.find((t) => t.id === type.id)?.color ||
              defaultKindColors[kind];
        this.typeReplacements = copy(saved?.replacements || {});
        this.typePending = saved?.pending || null;
        if (saved && saved.version !== value.version && !saved.pending)
          this.typeSettingsError =
            "Type settings changed elsewhere. Your draft is retained. Reload saved types before editing again.";
        if (value.diagnostics?.length)
          this.typeSettingsError = value.diagnostics.join("\n");
        if (reload) storage.set(this.typeDraftKey(), null);
        this.typeUsers = [...new Set([...this.typeUsers, user])].sort();
      } catch (e) {
        this.typeSettingsError = e.message;
      } finally {
        this.typeSettingsBusy = false;
      }
    },
    addType() {
      this.typeDraft[this.typeSettingsKind].push({
        id: "type_" + newID().slice(0, 12),
        name: "",
        description: "",
        color: this.typeKindColors[this.typeSettingsKind],
      });
      this.keepTypeDraft();
    },
    typeNameChanged(type) {
      const original = this.typeSettings.kinds[this.typeSettingsKind] || [];
      if (!original.some((entry) => entry.id === type.id)) {
        const slug = type.name
          .toLowerCase()
          .normalize("NFKD")
          .replace(/[\u0300-\u036f]/g, "")
          .replace(/[^a-z0-9]+/g, "_")
          .replace(/^_+|_+$/g, "");
        let id = /^[a-z]/.test(slug) ? slug : type.id;
        if (
          this.typeDraft[this.typeSettingsKind].some(
            (entry) => entry !== type && entry.id === id,
          )
        )
          id += "_" + newID().slice(0, 6);
        type.id = id;
      }
      this.keepTypeDraft();
    },
    removeType(id) {
      this.typeDraft[this.typeSettingsKind] = this.typeDraft[
        this.typeSettingsKind
      ].filter((t) => t.id !== id);
      this.keepTypeDraft();
    },
    removedUsedTypes() {
      if (!this.typeSettings || !this.typeDraft) return [];
      return this.typeKinds.flatMap((kind) =>
        Object.entries(this.typeSettings.usage?.[kind] || {})
          .filter(
            ([id, count]) =>
              count > 0 &&
              this.typeSettings.kinds[kind].some((t) => t.id === id) &&
              !this.typeDraft[kind].some((t) => t.id === id),
          )
          .map(([id, count]) => ({
            kind,
            id,
            count,
            name:
              this.typeSettings.kinds[kind].find((t) => t.id === id)?.name ||
              id,
          })),
      );
    },
    setTypeReplacement(kind, id, value) {
      this.typeReplacements[kind] ||= {};
      this.typeReplacements[kind][id] = value;
      this.keepTypeDraft();
    },
    async saveTypeSettings() {
      if (this.typeSettingsBusy || !this.typeDraft) return;
      this.typeSettingsBusy = true;
      this.typeSettingsError = this.typeSettingsNotice = "";
      try {
        this.typePending ||= {
          key: newID(),
          version: this.typeSettings.version,
          kinds: copy(this.typeDraft),
          kind_colors: copy(this.typeKindColors),
          my_entity: this.typeMyEntity,
          replacements: copy(this.typeReplacements),
        };
        this.keepTypeDraft();
        const submitted = this.typePending;
        const value = await request(
          "/api/types/" + encodeURIComponent(this.typeSettings.user),
          { method: "PUT", body: JSON.stringify(submitted) },
        );
        this.typePending = null;
        const compared = copy(value.kinds);
        for (const kind of this.typeKinds)
          for (const type of compared[kind] || [])
            if (
              !Object.hasOwn(
                submitted.kinds[kind]?.find((t) => t.id === type.id) || {},
                "color",
              )
            )
              delete type.color;
        if (
          value.my_entity !== submitted.my_entity ||
          JSON.stringify(compared) !== JSON.stringify(submitted.kinds) ||
          (submitted.kind_colors &&
            JSON.stringify(value.kind_colors) !==
              JSON.stringify(submitted.kind_colors))
        )
          throw Error(
            "Your previous save is confirmed, but settings changed afterwards. Reload saved types to compare; your draft is retained.",
          );
        const newer =
          this.typeMyEntity !== submitted.my_entity ||
          JSON.stringify(this.typeKindColors) !==
            JSON.stringify(submitted.kind_colors) ||
          JSON.stringify(this.typeDraft) !== JSON.stringify(submitted.kinds) ||
          JSON.stringify(this.typeReplacements) !==
            JSON.stringify(submitted.replacements);
        this.typeSettings = value;
        if (newer) {
          this.typeSettingsNotice =
            "Previous save confirmed. Your newer choices are still a draft.";
          this.keepTypeDraft();
        } else {
          this.typeDraft = copy(value.kinds);
          this.typeKindColors = copy(value.kind_colors);
          this.typeMyEntity = value.my_entity;
          this.typeReplacements = {};
          storage.set(this.typeDraftKey(), null);
          this.typeSettingsNotice = "Types saved for " + value.user + ".";
        }
        if (this.classificationUser === value.user) {
          await this.loadClassificationPolicy();
          await Promise.all([this.refresh(), this.refreshGraph()]);
        }
      } catch (e) {
        if (e.rejected) this.typePending = null;
        this.typeSettingsError = e.message;
        this.keepTypeDraft();
      } finally {
        this.typeSettingsBusy = false;
      }
    },
  };
}
