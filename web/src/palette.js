import palette from "../../palette.json";
export { palette };
export const defaultKindColors = {
  entities: "green",
  memories: "peach",
  topics: "lavender",
  stories: "red",
};
export const colorPresets = Object.entries(palette.mocha)
  .filter(([id]) => !["crust", "mantle", "base"].includes(id))
  .map(([id, hex]) => ({
    id,
    name: id[0].toUpperCase() + id.slice(1).replace(/(\d)/g, " $1"),
    hex,
  }));
export function isPreset(value) {
  return colorPresets.some((color) => color.id === value);
}
export function mochaColor(value) {
  return isPreset(value)
    ? palette.mocha[value]
    : /^#[\da-f]{6}$/i.test(value || "")
      ? value
      : palette.mocha.text;
}
export function colorCSS(value) {
  return isPreset(value)
    ? `var(--ctp-${value})`
    : /^#[\da-f]{6}$/i.test(value || "")
      ? value
      : "var(--ctp-text)";
}
export function currentPalette() {
  const bg = getComputedStyle(document.documentElement).backgroundColor;
  const hex = palette.mocha.crust;
  const rgb = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
  return bg === `rgb(${rgb.join(", ")})` ? palette.mocha : palette.latte;
}
export function resolveColor(value, colors = currentPalette()) {
  return (
    colors[value] || (/^#[\da-f]{6}$/i.test(value || "") ? value : colors.text)
  );
}
