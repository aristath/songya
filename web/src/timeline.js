const day = (value) => (value ? Date.parse(value) : NaN);
const known = (value) => Number.isFinite(value);

// The screen needs a reading order, but only dates and usable links establish
// chronology. Keep those links explicit and never write inferred dates back.
export function arrangeTimeline(records, constraints) {
  const entries = records.map((record) => {
    const time = record.time || {};
    let low = day(time.date || time.earliest),
      high = day(time.date || time.latest);
    const issues = [...(record.issues || [])];
    if (known(low) && known(high) && low > high) {
      issues.push("The earliest date is after the latest date.");
      low = high = NaN;
    }
    if (
      time.date &&
      ((time.earliest && time.earliest !== time.date) ||
        (time.latest && time.latest !== time.date))
    ) {
      issues.push("The exact date conflicts with its date range.");
      low = high = NaN;
    }
    return {
      ...record,
      relations: record.relations || [],
      issues,
      low,
      high,
      links: [],
      incoming: 0,
      next: [],
    };
  });
  const byID = new Map(entries.map((entry) => [entry.id, entry]));
  for (const edge of constraints) {
    const before = byID.get(edge.before),
      after = byID.get(edge.after);
    if (!before || !after) continue;
    const caused = edge.reason === "cause precedes consequence";
    before.links.push({
      id: after.id,
      title: after.title,
      relation: caused ? "Led to" : "Before",
      usable: edge.usable,
    });
    after.links.push({
      id: before.id,
      title: before.title,
      relation: caused ? "Caused by" : "After",
      usable: edge.usable,
    });
    if (edge.usable) {
      before.next.push(after);
      after.incoming++;
    } else {
      before.issues.push("A timing link conflicts with dates or forms a loop.");
      after.issues.push("A timing link conflicts with dates or forms a loop.");
    }
  }
  for (const entry of entries) {
    for (const link of entry.order_links || []) {
      const relation = link.relation === "caused_by" ? "Caused by" : "After";
      if (
        entry.links.some(
          (existing) =>
            existing.id === link.id && existing.relation === relation,
        )
      )
        continue;
      const edge = constraints.find(
        (edge) => edge.before === link.id && edge.after === entry.id,
      );
      entry.links.push({
        id: link.id,
        title: link.title || link.id,
        relation,
        usable: edge ? edge.usable : link.present ? null : false,
        missing: !link.present,
        outside: link.present && !byID.has(link.id),
      });
    }
  }
  // First establish the graph order, then propagate calendar bounds through it.
  const remaining = new Map(entries.map((entry) => [entry.id, entry.incoming]));
  const order = entries.filter((entry) => !entry.incoming);
  for (let cursor = 0; cursor < order.length; cursor++) {
    const entry = order[cursor];
    for (const next of entry.next) {
      if (known(entry.low) && (!known(next.low) || next.low < entry.low))
        next.low = entry.low;
      remaining.set(next.id, remaining.get(next.id) - 1);
      if (!remaining.get(next.id)) order.push(next);
    }
  }
  for (const entry of [...order].reverse())
    for (const next of entry.next)
      if (known(next.high) && (!known(entry.high) || entry.high > next.high))
        entry.high = next.high;
  const ready = entries.filter((entry) => !entry.incoming),
    sorted = [];
  const anchor = (entry) =>
    known(entry.low) ? entry.low : known(entry.high) ? entry.high : Infinity;
  while (ready.length) {
    ready.sort(
      (a, b) =>
        anchor(a) - anchor(b) ||
        a.title.localeCompare(b.title) ||
        a.id.localeCompare(b.id),
    );
    const entry = ready.shift();
    sorted.push(entry);
    for (const next of entry.next) if (!--next.incoming) ready.push(next);
  }
  for (const entry of entries)
    if (entry.incoming) {
      entry.issues.push("The order forms a loop and cannot be used.");
      sorted.push(entry);
    }
  for (const entry of sorted) {
    const time = entry.time || {};
    entry.issues = [...new Set(entry.issues)];
    entry.timing = entry.issues.length
      ? "conflict"
      : time.date
        ? "exact"
        : time.earliest || time.latest
          ? "range"
          : "unknown";
    entry.dateLabel =
      time.date ||
      (time.earliest && time.latest
        ? time.earliest + " – " + time.latest
        : time.earliest
          ? "On or after " + time.earliest
          : time.latest
            ? "On or before " + time.latest
            : "Date unknown");
    entry.section = known(anchor(entry))
      ? "dated"
      : entry.issues.length
        ? "review"
        : entry.links.some((link) => link.usable !== false)
          ? "relative"
          : "undated";
    entry.linkPlacement =
      entry.timing === "unknown" && entry.section === "dated";
    delete entry.next;
    delete entry.incoming;
  }
  return sorted;
}

export function timelineUI(request) {
  let generation = 0,
    controller,
    retryTimer,
    listKind = "";
  return {
    libraryView: "list",
    timelineRecords: [],
    timelineSubjects: [],
    timelineRelatedTo: "",
    timelineTiming: "",
    timelineBusy: false,
    timelineError: "",
    timelineDiagnostics: [],
    async setLibraryView(view) {
      if (view === this.libraryView) return;
      this.libraryView = view;
      if (view === "timeline") {
        listKind = this.kind;
        this.kind = "memories";
      } else {
        this.kind = listKind;
        generation++;
        controller?.abort();
        clearTimeout(retryTimer);
        this.timelineBusy = false;
      }
      await this.refresh();
    },
    async refreshTimeline() {
      if (this.libraryView !== "timeline") return true;
      const ticket = ++generation;
      controller?.abort();
      controller = new AbortController();
      clearTimeout(retryTimer);
      this.timelineBusy = true;
      try {
        const value = await request(
          "/api/timeline?" +
            new URLSearchParams({
              user: this.owner,
              classification_user: this.classificationUser,
            }),
          { signal: controller.signal },
        );
        if (ticket !== generation) return true;
        this.timelineRecords = arrangeTimeline(
          value.records,
          value.constraints,
        );
        this.timelineDiagnostics = value.diagnostics || [];
        this.timelineError = "";
        return true;
      } catch (e) {
        if (ticket !== generation) return true;
        this.timelineError = e.message;
        if (!e.status || e.status >= 500)
          retryTimer = setTimeout(() => this.refreshTimeline(), 1000);
        return false;
      } finally {
        if (ticket === generation) this.timelineBusy = false;
      }
    },
    timelineEntries() {
      const visible = new Set(this.rows().map((record) => record.id));
      return this.timelineRecords.filter(
        (entry) =>
          visible.has(entry.id) &&
          (!this.timelineTiming || this.timelineTiming === entry.timing) &&
          (!this.timelineRelatedTo ||
            entry.relations.some(
              (relation) => relation.target === this.timelineRelatedTo,
            )),
      );
    },
    timelineSections() {
      const entries = this.timelineEntries();
      return [
        {
          id: "dated",
          title: "Chronology",
          note: "Recorded dates and ranges. Undated entries can be placed using their links; no dates are filled in.",
        },
        {
          id: "relative",
          title: "Known order, dates unknown",
          note: "These memories have before/after links, but no calendar placement in this view.",
        },
        {
          id: "undated",
          title: "Date and order unknown",
          note: "These memories need a date, a date range, or a before/after link before they can be placed.",
        },
        {
          id: "review",
          title: "Timing needs review",
          note: "These dates or links conflict or cannot be read. The memories remain available to correct.",
        },
      ]
        .map((section) => ({
          ...section,
          entries: entries.filter((entry) => entry.section === section.id),
        }))
        .filter((section) => section.entries.length);
    },
    async editTimelineDates(id) {
      await this.open(id);
      if (this.selected?.id !== id) return;
      if (this.mode !== "visual") await this.switchMode("visual");
      this.showMetadata = true;
      this.showConnections = false;
      this.$nextTick(() =>
        this.$refs.editorDialog
          .querySelector('[aria-label="Exact event date"]')
          ?.focus(),
      );
    },
    async editTimelineOrder(id) {
      await this.open(id);
      if (this.selected?.id !== id) return;
      if (this.mode !== "visual") await this.switchMode("visual");
      this.showMetadata = false;
      this.showConnections = true;
      this.relation = "after";
      this.target = null;
      this.targetQuery = "";
      await this.searchTargets();
    },
  };
}
