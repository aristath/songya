import { newID } from "./ids.js";

const pad = (value, width = 2) => String(value).padStart(width, "0");
const stamp = (year, month, day) =>
  `${pad(year, 4)}-${pad(month + 1)}-${pad(day)}`;
const calendarDate = (year, month, day) => {
  const date = new Date(0);
  date.setUTCFullYear(year, month, day);
  return date;
};

export function normalizeDate(value) {
  const text = String(value || "").trim();
  if (!text) return "";
  const parts =
    text.match(/^(\d{4})-?(\d{2})-?(\d{2})$/) ||
    text.match(/^(\d{4})-(\d{1,2})-(\d{1,2})$/);
  if (!parts) return null;
  const [year, month, day] = parts.slice(1).map(Number);
  const date = calendarDate(year, month - 1, day);
  return date.getUTCFullYear() === year &&
    date.getUTCMonth() === month - 1 &&
    date.getUTCDate() === day
    ? stamp(year, month - 1, day)
    : null;
}

export function localDate(value) {
  if (value === null || value === undefined || value === "") return "Unknown";
  if (typeof value === "string" && /^\d{4}-\d{2}-\d{2}$/.test(value))
    return normalizeDate(value) || "Unknown";
  const date = new Date(value);
  return Number.isFinite(date.getTime())
    ? stamp(date.getFullYear(), date.getMonth(), date.getDate())
    : "Unknown";
}

export function localDateTime(value) {
  if (value === null || value === undefined || value === "") return "Unknown";
  const date = new Date(value);
  return Number.isFinite(date.getTime())
    ? `${localDate(date)} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
    : "Unknown";
}

// Keep the visible text and calendar under app control: native date fields
// choose their own display order from the browser locale.
export function registerDates(Alpine) {
  Alpine.directive(
    "iso-date",
    (input, { expression }, { effect, evaluateLater, cleanup }) => {
      const label = input.getAttribute("aria-label") || "Date";
      const id = "date-calendar-" + newID();
      const wrapper = document.createElement("span");
      wrapper.className = "iso-date";
      const opener = document.createElement("button");
      opener.type = "button";
      opener.className = "iso-date-open";
      opener.setAttribute("aria-label", label + " calendar");
      opener.setAttribute("aria-haspopup", "dialog");
      opener.setAttribute("aria-controls", id);
      opener.setAttribute("aria-expanded", "false");
      opener.innerHTML =
        '<svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true"><rect x="3" y="5" width="18" height="16" rx="2" fill="none" stroke="currentColor" stroke-width="1.5"/><path d="M7 2v6m10-6v6M3 11h18" fill="none" stroke="currentColor" stroke-width="1.5"/></svg>';
      const popup = document.createElement("div");
      popup.id = id;
      popup.className = "iso-calendar";
      popup.setAttribute("popover", "auto");
      popup.setAttribute("role", "dialog");
      popup.setAttribute("aria-label", label + " calendar");
      popup.innerHTML = `<div class="iso-calendar-heading"><button type="button" data-move="-1" aria-label="Previous month">‹</button><input class="iso-calendar-year" type="number" min="0" max="9999" aria-label="Calendar year"><span aria-hidden="true">-</span><select class="iso-calendar-month" aria-label="Calendar month"></select><button type="button" data-move="1" aria-label="Next month">›</button></div><p class="iso-calendar-period" id="${id}-period" aria-live="polite"></p><div class="iso-calendar-grid" role="grid" aria-labelledby="${id}-period"></div><div class="iso-calendar-footer"><button type="button" data-today>Today</button><button type="button" data-clear>Clear</button><button type="button" data-close>Close</button></div>`;
      const yearInput = popup.querySelector(".iso-calendar-year");
      yearInput.required = true;
      const monthInput = popup.querySelector(".iso-calendar-month");
      const period = popup.querySelector(".iso-calendar-period");
      const grid = popup.querySelector(".iso-calendar-grid");
      for (let i = 0; i < 12; i++)
        monthInput.add(new Option(pad(i + 1), String(i)));
      let year,
        month,
        focusDay = 1;
      const open = () => popup.matches(":popover-open");
      const close = (focus = false) => {
        if (open()) popup.hidePopover();
        if (focus) input.focus();
      };
      const validate = (value) =>
        input.setCustomValidity(
          normalizeDate(value) === null
            ? "Use a real date in YYYY-MM-DD format."
            : "",
        );
      const position = () => {
        if (!open()) return;
        const anchor = input.getBoundingClientRect(),
          box = popup.getBoundingClientRect();
        popup.style.left =
          Math.max(8, Math.min(anchor.left, innerWidth - box.width - 8)) + "px";
        popup.style.top =
          Math.max(
            8,
            Math.min(
              anchor.bottom + 4 + box.height <= innerHeight - 8
                ? anchor.bottom + 4
                : anchor.top - box.height - 4,
              innerHeight - box.height - 8,
            ),
          ) + "px";
      };
      const choose = (value) => {
        input.value = value;
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.dispatchEvent(new Event("change", { bubbles: true }));
        close(true);
      };
      const render = (focus = false) => {
        yearInput.value = pad(year, 4);
        monthInput.value = String(month);
        period.textContent = `${pad(year, 4)}-${pad(month + 1)}`;
        const first = (calendarDate(year, month, 1).getUTCDay() + 6) % 7;
        const count = calendarDate(year, month + 1, 0).getUTCDate();
        focusDay = Math.max(1, Math.min(focusDay, count));
        const selected = normalizeDate(input.value);
        grid.replaceChildren();
        const header = document.createElement("div");
        header.className = "iso-calendar-week";
        header.setAttribute("role", "row");
        for (const day of ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]) {
          const cell = document.createElement("span");
          cell.setAttribute("role", "columnheader");
          cell.textContent = day;
          header.append(cell);
        }
        grid.append(header);
        for (let start = 0; start < first + count; start += 7) {
          const row = document.createElement("div");
          row.className = "iso-calendar-week";
          row.setAttribute("role", "row");
          for (let col = 0; col < 7; col++) {
            const day = start + col - first + 1;
            const cell = document.createElement("span");
            cell.setAttribute("role", "gridcell");
            if (day > 0 && day <= count) {
              const button = document.createElement("button");
              button.type = "button";
              button.textContent = String(day);
              button.dataset.date = stamp(year, month, day);
              button.setAttribute("aria-label", button.dataset.date);
              cell.setAttribute(
                "aria-selected",
                String(button.dataset.date === selected),
              );
              button.tabIndex = day === focusDay ? 0 : -1;
              button.addEventListener(
                "click",
                () => yearInput.reportValidity() && choose(button.dataset.date),
              );
              cell.append(button);
            }
            row.append(cell);
          }
          grid.append(row);
        }
        popup.querySelector('[data-move="-1"]').disabled =
          year === 0 && month === 0;
        popup.querySelector('[data-move="1"]').disabled =
          year === 9999 && month === 11;
        position();
        if (focus)
          grid
            .querySelector(`[data-date="${stamp(year, month, focusDay)}"]`)
            ?.focus();
      };
      const moveMonth = (delta, focus = false) => {
        const date = calendarDate(year, month + delta, 1);
        if (date.getUTCFullYear() < 0 || date.getUTCFullYear() > 9999) return;
        year = date.getUTCFullYear();
        month = date.getUTCMonth();
        render(focus);
      };
      opener.addEventListener("click", () => {
        if (open()) return close(true);
        const selected = normalizeDate(input.value) || localDate(new Date());
        [year, month, focusDay] = selected.split("-").map(Number);
        month--;
        render();
        popup.showPopover();
        position();
        grid
          .querySelector(`[data-date="${stamp(year, month, focusDay)}"]`)
          ?.focus();
      });
      popup
        .querySelectorAll("[data-move]")
        .forEach((button) =>
          button.addEventListener("click", () =>
            moveMonth(Number(button.dataset.move)),
          ),
        );
      yearInput.addEventListener("change", () => {
        if (yearInput.checkValidity() && yearInput.value !== "") {
          const next = Number(yearInput.value);
          if (next !== year) {
            year = next;
            render();
          }
        }
      });
      monthInput.addEventListener("change", () => {
        const next = Number(monthInput.value);
        if (next !== month) {
          month = next;
          render();
        }
      });
      popup
        .querySelector("[data-today]")
        .addEventListener("click", () => choose(localDate(new Date())));
      popup
        .querySelector("[data-clear]")
        .addEventListener("click", () => choose(""));
      popup
        .querySelector("[data-close]")
        .addEventListener("click", () => close(true));
      popup.addEventListener("keydown", (event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          close(true);
          return;
        }
        if (!event.target.dataset.date) return;
        let delta = { ArrowLeft: -1, ArrowRight: 1, ArrowUp: -7, ArrowDown: 7 }[
          event.key
        ];
        if (event.key === "Home")
          delta = -((calendarDate(year, month, focusDay).getUTCDay() + 6) % 7);
        if (event.key === "End")
          delta =
            6 - ((calendarDate(year, month, focusDay).getUTCDay() + 6) % 7);
        if (event.key === "PageUp" || event.key === "PageDown") {
          event.preventDefault();
          moveMonth(
            (event.key === "PageUp" ? -1 : 1) * (event.shiftKey ? 12 : 1),
            true,
          );
          return;
        }
        if (delta === undefined) return;
        event.preventDefault();
        const date = calendarDate(year, month, focusDay + delta);
        if (date.getUTCFullYear() < 0 || date.getUTCFullYear() > 9999) return;
        year = date.getUTCFullYear();
        month = date.getUTCMonth();
        focusDay = date.getUTCDate();
        render(true);
      });
      popup.addEventListener("focusout", () =>
        setTimeout(() => {
          if (open() && !popup.contains(document.activeElement)) close();
        }, 0),
      );
      popup.addEventListener("toggle", () => {
        opener.setAttribute("aria-expanded", String(open()));
        if (open()) {
          window.addEventListener("resize", position);
          document.addEventListener("scroll", position, true);
        } else {
          window.removeEventListener("resize", position);
          document.removeEventListener("scroll", position, true);
        }
      });
      const onInput = () => validate(input.value);
      const onChange = () => {
        const value = normalizeDate(input.value);
        if (value !== null && value !== input.value) {
          input.value = value;
          input.dispatchEvent(new Event("input", { bubbles: true }));
        }
        validate(input.value);
      };
      input.type = "text";
      input.classList.add("iso-date-input");
      input.placeholder = "YYYY-MM-DD";
      input.maxLength = 10;
      input.autocomplete = "off";
      input.setAttribute("pattern", "[0-9]{4}-[0-9]{2}-[0-9]{2}");
      input.addEventListener("input", onInput);
      input.addEventListener("change", onChange);
      Alpine.mutateDom(() => {
        input.before(wrapper);
        wrapper.append(input, opener, popup);
      });
      const value = evaluateLater(expression);
      effect(() => value((current) => validate(current)));
      cleanup(() => {
        close();
        window.removeEventListener("resize", position);
        document.removeEventListener("scroll", position, true);
        input.removeEventListener("input", onInput);
        input.removeEventListener("change", onChange);
      });
    },
  ).before("model");
}
