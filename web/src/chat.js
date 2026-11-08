import MarkdownIt from "markdown-it";
import { newID } from "./ids.js";

const markdown = new MarkdownIt({ html: false, linkify: true, breaks: true });
const local = {
  get(key, fallback = "") {
    try {
      return localStorage.getItem(key) ?? fallback;
    } catch {
      return fallback;
    }
  },
  set(key, value) {
    try {
      if (value === null) localStorage.removeItem(key);
      else localStorage.setItem(key, value);
    } catch {
      /* Accepted messages are stored by the server. */
    }
  },
};
export function chatUI(request) {
  let events,
    recordEvents,
    recordsTimer,
    watch = 0,
    settingsGeneration = 0,
    historyObserver,
    historyTimer,
    historyListing = 0;
  return {
    chatUser: local.get("songya-chat-user"),
    chatSessions: [],
    chatSessionID: "",
    chatState: null,
    chatInput: "",
    chatSending: false,
    chatPending: null,
    chatError: "",
    chatConnection: "",
    chatFollow: true,
    chatAddingUser: false,
    chatNewUser: "",
    chatDraftUser: "",
    chatDraftSession: "",
    chatSearch: "",
    chatArchived: false,
    chatCompact: false,
    chatExpanded: false,
    chatManaging: false,
    chatRename: "",
    chatSettings: {
      base_url: "",
      model: "",
      temperature: 0.15,
      configured: false,
      has_api_key: false,
    },
    chatSettingsDraft: {
      base_url: "",
      model: "",
      temperature: 0.15,
      api_key: "",
      clear_key: false,
    },
    chatSettingsBusy: false,
    chatSettingsError: "",
    chatSettingsNotice: "",
    chatModels: [],
    async initChat() {
      this.watchRecords();
      const generation = settingsGeneration;
      await Promise.allSettled([
        request("/api/chat/settings")
          .then((value) => {
            if (generation === settingsGeneration) this.chatSettings = value;
          })
          .catch((e) => {
            this.chatError = e.message;
          }),
        this.refreshChatSessions(),
      ]);
      if (!this.chatUser) this.chatUser = this.chatUsers()[0] || "";
      try {
        this.chatPending = JSON.parse(local.get("songya-chat-pending", "null"));
      } catch {
        this.chatPending = null;
      }
      if (this.chatPending) {
        this.chatUser = this.chatPending.user;
        this.chatSessionID = this.chatPending.id;
        this.loadChatDraft();
        await this.selectChat();
        await this.flushChatPending();
      } else await this.changeChatUser();
      this.$nextTick(() => this.initChatLayout());
    },
    initChatLayout() {
      historyObserver?.disconnect();
      historyObserver = new ResizeObserver(() => {
        const probe = this.$refs.chatFit;
        const compact =
          probe.lastElementChild.offsetTop > probe.firstElementChild.offsetTop;
        const dialog = this.$refs.chatSidebar;
        if (compact !== this.chatCompact && dialog.open) dialog.close();
        this.chatCompact = compact;
        if (!compact && !dialog.open) dialog.show();
      });
      historyObserver.observe(this.$refs.chatWorkspace);
    },
    openChatSidebar() {
      this.refreshChatSessions();
      if (!this.$refs.chatSidebar.open) this.$refs.chatSidebar.showModal();
      this.$refs.chatSearch.focus();
    },
    closeChatSidebar() {
      if (this.chatCompact) this.$refs.chatSidebar.close();
    },
    toggleChatExpanded() {
      this.chatExpanded = !this.chatExpanded;
      this.$nextTick(() => this.$refs.chatExpand.focus());
    },
    chatNavigationBusy() {
      return this.chatSending || !!this.chatPending || this.chatManaging;
    },
    chatDraftKey() {
      return (
        "songya-chat-draft:" +
        this.chatDraftUser +
        (this.chatDraftSession ? ":" + this.chatDraftSession : "")
      );
    },
    loadChatDraft() {
      this.chatDraftUser = this.chatUser;
      this.chatDraftSession = this.chatSessionID;
      this.chatInput = local.get(this.chatDraftKey());
    },
    async openChat(id) {
      if (this.chatNavigationBusy()) return;
      this.keepChatDraft();
      this.chatSessionID = id;
      local.set("songya-chat-selected:" + this.chatUser, id);
      this.loadChatDraft();
      await this.selectChat();
      this.closeChatSidebar();
      if (!id) this.$refs.chatInput?.focus();
    },
    newChat() {
      this.chatArchived = false;
      this.openChat("");
    },
    chatSessionStatus(conversation) {
      if (["pending", "running"].includes(conversation.status))
        return conversation.queued
          ? `Working · ${conversation.queued} queued`
          : "Working";
      if (conversation.status === "paused") return "Paused";
      if (conversation.status === "error") return "Needs attention";
      return "";
    },
    chatSessionDate(conversation) {
      return new Date(conversation.updated_at).toLocaleDateString(undefined, {
        month: "short",
        day: "numeric",
      });
    },
    openChatRename() {
      this.$refs.chatActions.removeAttribute("open");
      this.chatRename = this.chatState.title;
      this.$refs.chatRenameDialog.showModal();
      this.$nextTick(() => {
        this.$refs.chatRenameInput.focus();
        this.$refs.chatRenameInput.select();
      });
    },
    async updateChatSession(fields) {
      if (!this.chatState || this.chatNavigationBusy()) return;
      this.chatManaging = true;
      this.chatError = "";
      try {
        const value = await request(
          "/api/chat/sessions/" + this.chatSessionID,
          { method: "PATCH", body: JSON.stringify(fields) },
        );
        this.applyChat(value);
        this.chatArchived = !!value.archived;
        this.$refs.chatRenameDialog.close();
        this.$refs.chatActions.removeAttribute("open");
        await this.refreshChatSessions();
      } catch (e) {
        this.chatError = e.message;
      } finally {
        this.chatManaging = false;
      }
    },
    chatUsers() {
      return [
        ...new Set([
          ...Object.keys(this.owners),
          ...this.chatSessions.map((c) => c.user),
          ...(this.chatUser ? [this.chatUser] : []),
        ]),
      ].sort();
    },
    chatHistory() {
      const search = this.chatSearch.trim().toLocaleLowerCase();
      return this.chatSessions.filter(
        (c) =>
          c.user === this.chatUser &&
          !!c.archived === this.chatArchived &&
          (!search ||
            (c.title + " " + (c.preview || ""))
              .toLocaleLowerCase()
              .includes(search)),
      );
    },
    async refreshChatSessions() {
      const ticket = ++historyListing;
      clearTimeout(historyTimer);
      try {
        const value = await request("/api/chat/sessions");
        if (ticket !== historyListing) return;
        this.chatSessions = value.sessions;
        if (value.diagnostics?.length)
          this.chatError = value.diagnostics.join("\n");
      } catch (e) {
        if (ticket === historyListing) this.chatError = e.message;
      } finally {
        if (
          ticket === historyListing &&
          this.chatSessions.some((c) =>
            ["pending", "running"].includes(c.status),
          )
        )
          historyTimer = setTimeout(() => this.refreshChatSessions(), 3000);
      }
    },
    destroy() {
      clearTimeout(historyTimer);
      clearTimeout(recordsTimer);
      recordEvents?.close();
      historyObserver?.disconnect();
      events?.close();
    },
    watchRecords() {
      recordEvents?.close();
      recordEvents = new EventSource("/api/records/events");
      recordEvents.onmessage = () => this.refreshChatRecords();
    },
    async refreshChatRecords() {
      clearTimeout(recordsTimer);
      const results = await Promise.allSettled([
        this.refreshGraph(),
        this.refresh(),
      ]);
      if (
        results.some(
          (result) => result.status === "rejected" || result.value === false,
        )
      )
        recordsTimer = setTimeout(() => this.refreshChatRecords(), 1000);
    },
    async changeChatUser() {
      this.keepChatDraft();
      local.set("songya-chat-user", this.chatUser);
      const selected = local.get("songya-chat-selected:" + this.chatUser);
      this.chatSessionID = this.chatSessions.some(
        (c) => c.id === selected && c.user === this.chatUser,
      )
        ? selected
        : "";
      this.chatArchived = !!this.chatSessions.find(
        (c) => c.id === this.chatSessionID,
      )?.archived;
      this.chatSearch = "";
      this.loadChatDraft();
      await this.selectChat();
    },
    addChatUser() {
      if (this.chatSending || this.chatPending) return;
      const user = this.chatNewUser.trim();
      if (!user || user === "all") {
        this.chatError =
          "Enter one person's user name; ‘all’ selects multiple collections.";
        return;
      }
      this.chatUser = user;
      this.chatNewUser = "";
      this.chatAddingUser = false;
      this.changeChatUser();
    },
    async selectChat() {
      events?.close();
      watch++;
      this.chatState = null;
      this.chatError = "";
      this.chatConnection = "";
      this.chatFollow = true;
      if (!this.chatSessionID) return;
      const ticket = watch;
      try {
        const value = await request("/api/chat/sessions/" + this.chatSessionID);
        if (ticket !== watch) return;
        this.applyChat(value);
        if (["pending", "running"].includes(value.status)) this.watchChat();
      } catch (e) {
        if (ticket === watch) this.chatError = e.message;
      }
    },
    chatBusy() {
      return ["pending", "running"].includes(this.chatState?.status);
    },
    chatMessages() {
      const messages = this.chatState?.messages || [];
      const visible = messages.filter(
        (m) => m.role === "user" || m.role === "assistant",
      );
      const queue = this.chatState?.queue || [];
      const known = new Set([
        ...messages.map((m) => m.turn_id),
        ...queue.map((m) => m.id),
      ]);
      return [
        ...visible,
        ...(this.chatState?.draft ? [this.chatState.draft] : []),
        ...queue.map((m) => ({
          role: "user",
          content: m.text,
          status: "Queued",
        })),
        ...(this.chatPending
          ? [this.chatPending, ...(this.chatPending.next || [])].flatMap(
              (m, i) =>
                known.has(m.request_id)
                  ? []
                  : [
                      {
                        role: "user",
                        content: m.text,
                        status: i
                          ? "Waiting to send"
                          : this.chatSending
                            ? "Sending…"
                            : "Not sent · retry",
                      },
                    ],
            )
          : []),
      ];
    },
    chatToolResult(id) {
      return (
        [...(this.chatState?.messages || [])]
          .reverse()
          .find((m) => m.role === "tool" && m.tool_call_id === id)?.content ||
        "Waiting for the tool result…"
      );
    },
    chatMarkdown(text) {
      return markdown.render(text || "");
    },
    applyChat(value) {
      if (
        this.chatState?.id === value.id &&
        this.chatState.revision > value.revision
      )
        return;
      const follow = this.chatFollow;
      this.chatState = value;
      this.chatSessionID = value.id;
      local.set("songya-chat-selected:" + value.user, value.id);
      this.chatConnection = "";
      const existing = this.chatSessions.find((c) => c.id === value.id);
      const summary = {
        id: value.id,
        user: value.user,
        title: value.title,
        status: value.status,
        updated_at: value.updated_at,
        archived: value.archived,
        queued: value.queue?.length || 0,
        preview:
          [...value.messages]
            .reverse()
            .find(
              (m) =>
                ["user", "assistant"].includes(m.role) && m.content?.trim(),
            )?.content || "",
      };
      if (existing) Object.assign(existing, summary);
      else this.chatSessions.unshift(summary);
      this.$nextTick(() => {
        if (follow) {
          this.scrollChat();
          this.chatFollow = true;
          // Layout can shrink the log when progress controls become visible.
          requestAnimationFrame(() => {
            if (this.chatFollow) this.scrollChat();
          });
        }
      });
      if (!this.chatBusy()) {
        events?.close();
        this.refreshChatSessions();
      }
    },
    scrollChat() {
      const log = this.$refs.chatLog;
      if (log) log.scrollTop = log.scrollHeight;
    },
    followChat() {
      this.chatFollow = true;
      this.scrollChat();
    },
    keepChatDraft() {
      if (this.chatDraftUser) local.set(this.chatDraftKey(), this.chatInput);
    },
    watchChat() {
      events?.close();
      const ticket = ++watch;
      events = new EventSource(
        "/api/chat/sessions/" + this.chatSessionID + "/events",
      );
      events.onmessage = (event) => {
        if (ticket === watch) this.applyChat(JSON.parse(event.data));
      };
      events.onerror = () => {
        if (ticket === watch && this.chatBusy())
          this.chatConnection = "Reconnecting… Your accepted message is saved.";
      };
      events.addEventListener("unavailable", (event) => {
        if (ticket !== watch) return;
        this.chatError = JSON.parse(event.data).error;
        this.chatConnection = "Conversation temporarily unavailable.";
        events.close();
      });
    },
    async sendChat() {
      if (
        !this.chatUser ||
        !this.chatSettings.configured ||
        this.chatState?.archived
      )
        return;
      if (this.chatInput.trim()) {
        const message = {
          id: this.chatSessionID || this.chatPending?.id || newID(),
          request_id: newID(),
          user: this.chatUser,
          text: this.chatInput,
        };
        this.chatPending = this.chatPending
          ? {
              ...this.chatPending,
              next: [...(this.chatPending.next || []), message],
            }
          : message;
        local.set("songya-chat-pending", JSON.stringify(this.chatPending));
        this.chatInput = "";
        this.keepChatDraft();
        this.chatSessionID = message.id;
        this.chatDraftSession = message.id;
        local.set("songya-chat-selected:" + this.chatUser, message.id);
        this.chatFollow = true;
        this.$refs.chatInput?.focus();
        this.$nextTick(() => this.scrollChat());
      }
      await this.flushChatPending();
    },
    async flushChatPending() {
      if (this.chatSending || !this.chatPending) return;
      this.chatSending = true;
      this.chatError = "";
      try {
        while (this.chatPending) {
          const { next, ...message } = this.chatPending;
          const value = await request("/api/chat", {
            method: "POST",
            body: JSON.stringify(message),
          });
          const [head, ...tail] = this.chatPending.next || [];
          this.chatPending = head ? { ...head, next: tail } : null;
          local.set(
            "songya-chat-pending",
            this.chatPending ? JSON.stringify(this.chatPending) : null,
          );
          this.applyChat(value);
          if (this.chatBusy()) this.watchChat();
        }
      } catch (e) {
        this.chatError = e.message + " Retry sends the same request.";
      } finally {
        this.chatSending = false;
      }
    },
    async retryChat() {
      this.chatError = "";
      const ticket = watch;
      const id = this.chatSessionID;
      try {
        const value = await request("/api/chat/sessions/" + id + "/retry", {
          method: "POST",
        });
        if (ticket !== watch) return;
        this.chatFollow = true;
        this.applyChat(value);
        this.watchChat();
      } catch (e) {
        if (ticket === watch) this.chatError = e.message;
      }
    },
    async pauseChat() {
      const ticket = watch;
      const id = this.chatSessionID;
      try {
        const value = await request("/api/chat/sessions/" + id + "/pause", {
          method: "POST",
        });
        if (ticket === watch) this.applyChat(value);
      } catch (e) {
        if (ticket === watch) this.chatError = e.message;
      }
    },
    openChatSettings() {
      this.chatSettingsDraft = {
        base_url: this.chatSettings.base_url,
        model: this.chatSettings.model,
        temperature: this.chatSettings.temperature ?? 0.15,
        api_key: "",
        clear_key: false,
      };
      this.chatSettingsError = "";
      this.chatSettingsNotice = "";
      this.$refs.chatSettingsDialog.showModal();
    },
    closeChatSettings() {
      this.$refs.chatSettingsDialog.close();
    },
    async saveChatSettings(close = true) {
      settingsGeneration++;
      this.chatSettingsBusy = true;
      this.chatSettingsError = "";
      this.chatSettingsNotice = "";
      try {
        const draft = this.chatSettingsDraft;
        const payload = {
          base_url: draft.base_url,
          model: draft.model,
          temperature: draft.temperature,
        };
        if (draft.api_key || draft.clear_key)
          payload.api_key = draft.clear_key ? "" : draft.api_key;
        this.chatSettings = await request("/api/chat/settings", {
          method: "PUT",
          body: JSON.stringify(payload),
        });
        this.chatSettingsDraft.api_key = "";
        this.chatSettingsDraft.clear_key = false;
        if (close) this.closeChatSettings();
        return true;
      } catch (e) {
        this.chatSettingsError = e.message;
        return false;
      } finally {
        this.chatSettingsBusy = false;
      }
    },
    async loadChatModels() {
      if (!(await this.saveChatSettings(false))) return;
      this.chatSettingsBusy = true;
      try {
        const value = await request("/api/chat/models");
        this.chatModels = value.models;
        this.chatSettingsNotice = `Connected. ${value.models.length} models available.`;
      } catch (e) {
        this.chatSettingsError = e.message;
      } finally {
        this.chatSettingsBusy = false;
      }
    },
  };
}
