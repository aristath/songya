import { build } from "esbuild";
import { readFileSync, writeFileSync, existsSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";

const result = await build({
  entryPoints: ["src/app.js"],
  bundle: true,
  minify: true,
  metafile: true,
  outfile: "assets/app.js",
  target: "es2022",
  legalComments: "linked",
});
const styles = [
  "prosemirror-view/style/prosemirror.css",
  "prosemirror-menu/style/menu.css",
  "prosemirror-example-setup/style/style.css",
]
  .map((path) => readFileSync("node_modules/" + path, "utf8"))
  .join("\n");
writeFileSync("assets/editors.css", styles);
// Keep license notices for every package actually present in the executable.
const packages = new Set();
for (const input of Object.keys(result.metafile.inputs)) {
  if (!input.startsWith("node_modules/")) continue;
  let directory = dirname(input);
  while (directory !== "." && !existsSync(join(directory, "package.json")))
    directory = dirname(directory);
  if (directory !== ".") packages.add(directory);
}
const notices = [...packages].sort().map((directory) => {
  const { name, version, license } = JSON.parse(
    readFileSync(join(directory, "package.json"), "utf8"),
  );
  const file = readdirSync(directory).find((file) =>
    /^licen[cs]e(?:\.|$)/i.test(file),
  );
  return `${name} ${version} (${license})\n${file ? readFileSync(join(directory, file), "utf8") : ""}`;
});
writeFileSync(
  "assets/NOTICE.txt",
  notices.join("\n\n----------------------------------------\n\n"),
);

// Official palette values are shared with Go's color validation and defaults.
const palette = JSON.parse(readFileSync("../palette.json", "utf8"));
writeFileSync(
  "assets/palette.css",
  ":root {\n" +
    Object.keys(palette.mocha)
      .map(
        (key) =>
          `  --ctp-${key}: light-dark(${palette.latte[key]}, ${palette.mocha[key]});`,
      )
      .join("\n") +
    "\n}\n",
);
const catppuccinLicense = readFileSync("../palette-LICENSE.txt", "utf8");
writeFileSync(
  "assets/NOTICE.txt",
  readFileSync("assets/NOTICE.txt", "utf8") +
    "\n\nCatppuccin palette - https://catppuccin.com/palette/\n" +
    catppuccinLicense,
);
