import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { generateKeyPairSync, sign } from "node:crypto";
import { chromium } from "playwright";

test(
  "Android connection UI pairs only the selected store and revokes the phone",
  { timeout: 120000 },
  async () => {
    const root = mkdtempSync(join(tmpdir(), "songya-mobile-browser-"));
    let processHandle, browser;
    try {
      const binary = join(root, "songya-web");
      execFileSync("go", ["build", "-o", binary, "./cmd/songya-web"], {
        cwd: resolve(".."),
      });
      writeFileSync(join(root, "app.apk"), "synthetic APK fixture");
      const config = join(root, "config.json");
      writeFileSync(
        config,
        JSON.stringify({
          stores: { ari: join(root, "ari"), other: join(root, "other") },
          password: "fixture-password",
          public_url: "https://songya.example",
          android_apk: join(root, "app.apk"),
        }),
      );
      processHandle = spawn(binary, [
        "--config",
        config,
        "--listen",
        "127.0.0.1:0",
      ]);
      const origin = await new Promise((resolve, reject) => {
        processHandle.stdout.on("data", (chunk) => {
          const m = String(chunk).match(/http:\/\/[^\s]+/);
          if (m) resolve(m[0]);
        });
        processHandle.once("exit", (code) =>
          reject(new Error("Server exited: " + code)),
        );
        processHandle.once("error", reject);
      });
      browser = await chromium.launch({ headless: true });
      const context = await browser.newContext({
        viewport: { width: 412, height: 915 },
        httpCredentials: { username: "admin", password: "fixture-password" },
      });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await page.goto(origin + "/stores/ari/");
      await page
        .getByRole("button", { name: "Connect phone", exact: true })
        .click();
      const dialog = page.getByRole("dialog", { name: "Songya on Android" });
      await dialog
        .getByRole("link", { name: "Download Android app" })
        .waitFor();
      await dialog
        .getByRole("button", { name: "Create pairing code", exact: true })
        .click();
      await dialog.locator(".mobile-qr").waitFor({ state: "visible" });
      const uri = await dialog
        .getByRole("link", { name: "Open in Songya on this phone" })
        .getAttribute("href");
      assert.match(uri, /^songya:\/\/pair\?/);
      assert.equal(
        new URL(uri).searchParams.get("url"),
        "https://songya.example",
      );
      assert.equal(
        await dialog
          .locator(".mobile-qr")
          .evaluate((img) => img.complete && img.naturalWidth > 0),
        true,
      );
      assert.ok((await dialog.boundingBox()).width <= 412);
      const json = async (path, body, cookie) => {
        const response = await fetch(origin + path, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            ...(cookie ? { Cookie: cookie } : {}),
          },
          body: JSON.stringify(body),
        });
        assert.equal(response.status, 200);
        return response.json();
      };
      const { publicKey, privateKey } = generateKeyPairSync("ec", {
        namedCurve: "prime256v1",
      });
      const challenge = await json("/api/mobile/challenge", {
        publicKey: publicKey
          .export({ format: "der", type: "spki" })
          .toString("base64"),
        label: "Browser fixture phone",
        pairingToken: new URL(uri).searchParams.get("token"),
      });
      const proof = await json("/api/mobile/authenticate", {
        id: challenge.id,
        signature: sign(
          "sha256",
          Buffer.from(challenge.message),
          privateKey,
        ).toString("base64"),
      });
      const handoff = await fetch(origin + "/api/mobile/session", {
        method: "POST",
        redirect: "manual",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({ handoff: proof.handoff }),
      });
      assert.equal(handoff.status, 303);
      assert.equal(handoff.headers.get("location"), "/stores/ari/");
      const cookie = handoff.headers.get("set-cookie").split(";")[0];
      const phoneContext = await browser.newContext();
      await phoneContext.addCookies([
        {
          name: "songya_session",
          value: cookie.split("=")[1],
          url: origin,
          httpOnly: true,
          secure: false,
        },
      ]);
      const phonePage = await phoneContext.newPage();
      await phonePage.goto(origin + "/stores/ari/");
      assert.equal(
        await phonePage
          .getByRole("combobox", { name: "Store", exact: true })
          .locator("option")
          .count(),
        1,
      );
      assert.equal(
        (
          await phoneContext.request.get(origin + "/stores/other/api/records")
        ).status(),
        404,
      );
      await dialog
        .getByRole("button", { name: "Refresh phones", exact: true })
        .click();
      await dialog
        .getByText("Browser fixture phone", { exact: true })
        .waitFor();
      page.once("dialog", (alert) => alert.accept());
      await dialog
        .getByRole("button", { name: "Revoke access", exact: true })
        .click();
      await page.waitForFunction(
        () =>
          Alpine.$data(document.querySelector('[x-data="songya"]')).mobileStatus
            .devices.length === 0,
      );
      assert.equal(
        (
          await fetch(origin + "/stores/ari/api/records", {
            headers: { Cookie: cookie },
          })
        ).status,
        401,
      );
      assert.deepEqual(errors, []);
    } finally {
      await browser?.close();
      if (processHandle?.exitCode === null) {
        processHandle.kill("SIGTERM");
        await once(processHandle, "exit");
      }
      rmSync(root, { recursive: true, force: true });
    }
  },
);
