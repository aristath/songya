export function recallUI(request) {
  return {
    recallSettings: null,
    recallDraft: null,
    recallBusy: false,
    recallError: "",
    recallNotice: "",
    recallMessage: "",
    recallResult: null,
    recallJournalBlocked: false,
    recallDraftKey() {
      return this.recallSettings
        ? "songya-recall-draft:" + this.recallSettings.config_file
        : "";
    },
    keepRecallDraft() {
      if (
        !this.recallSettings ||
        !this.recallDraft ||
        this.recallJournalBlocked
      )
        return;
      try {
        if (
          JSON.stringify(this.recallDraft) ===
          JSON.stringify(this.recallSettings.recall)
        )
          localStorage.removeItem(this.recallDraftKey());
        else
          localStorage.setItem(
            this.recallDraftKey(),
            JSON.stringify({
              version: this.recallSettings.version,
              recall: this.recallDraft,
            }),
          );
      } catch {
        /* The open form still retains its draft. */
      }
    },
    recallKinds: ["memories", "entities", "stories", "topics"],
    recallRelations: [
      "after",
      "caused_by",
      "part_of",
      "related_to",
      "possibly_same_as",
    ],
    async openRecallSettings(reload = false) {
      this.recallError = "";
      this.recallNotice = "";
      this.$refs.recallSettingsDialog.showModal();
      this.recallBusy = true;
      try {
        const value = await request("api/recall/settings");
        let saved =
          this.recallDraft && this.recallSettings
            ? { version: this.recallSettings.version, recall: this.recallDraft }
            : null;
        const key = "songya-recall-draft:" + value.config_file;
        this.recallJournalBlocked = false;
        try {
          const raw = localStorage.getItem(location.pathname + key);
          if (reload) {
            if (raw) localStorage.setItem(key + ":recovery:" + Date.now(), raw);
            localStorage.removeItem(location.pathname + key);
            saved = null;
          } else if (raw) {
            const candidate = JSON.parse(raw);
            if (
              !candidate ||
              typeof candidate.version !== "string" ||
              !candidate.recall ||
              Object.keys(value.recall).some(
                (k) => !Object.hasOwn(candidate.recall, k),
              )
            )
              throw Error("Unreadable saved retrieval draft");
            saved = candidate;
          }
        } catch (e) {
          this.recallJournalBlocked = true;
          this.recallError =
            "The saved retrieval draft could not be read. Its original data is retained. Reload the saved policy to continue.";
        }
        this.recallSettings = value;
        this.recallDraft = JSON.parse(
          JSON.stringify(saved?.recall || value.recall),
        );
        if (
          saved &&
          JSON.stringify(saved.recall) !== JSON.stringify(value.recall)
        ) {
          this.recallSettings = { ...value, version: saved.version };
          if (saved.version !== value.version)
            this.recallError =
              "Settings changed elsewhere. Your draft is retained; reload the saved policy to compare before saving.";
        }
        this.keepRecallDraft();
        if (value.diagnostics.length)
          this.recallError = value.diagnostics.join("\n");
      } catch (e) {
        this.recallError = e.message;
      } finally {
        this.recallBusy = false;
      }
    },
    async saveRecallSettings() {
      this.keepRecallDraft();
      this.recallBusy = true;
      this.recallError = "";
      this.recallNotice = "";
      try {
        this.recallSettings = await request("api/recall/settings", {
          method: "PUT",
          body: JSON.stringify({
            version: this.recallSettings.version,
            recall: this.recallDraft,
          }),
        });
        this.recallDraft = JSON.parse(
          JSON.stringify(this.recallSettings.recall),
        );
        this.keepRecallDraft();
        this.recallNotice =
          "Retrieval settings saved. New requests use this policy.";
      } catch (e) {
        this.recallError = e.message;
      } finally {
        this.recallBusy = false;
      }
    },
    async testRecall() {
      this.recallBusy = true;
      this.recallError = "";
      try {
        const response = await request("api/recall", {
          method: "POST",
          body: JSON.stringify({
            message: this.recallMessage,
          }),
        });
        this.recallResult = response.results[0];
      } catch (e) {
        this.recallError = e.message;
      } finally {
        this.recallBusy = false;
      }
    },
  };
}
