// Pair the current store with a phone; all memory screens remain in the web app.
export function mobileUI(request) {
  const store = () =>
    document.querySelector('meta[name="songya-store"]').content;
  let expiry;
  return {
    mobileStatus: { devices: [], public_url: "", apk_available: false },
    mobilePairing: null,
    mobileBusy: false,
    mobileError: "",
    async openMobile() {
      this.mobileError = "";
      this.mobilePairing = null;
      this.$refs.mobileDialog.showModal();
      try {
        this.mobileStatus = await request(
          "/api/mobile?store=" + encodeURIComponent(store()),
        );
      } catch (error) {
        this.mobileError = error.message;
      }
    },
    closeMobile() {
      clearTimeout(expiry);
      this.mobilePairing = null;
      this.$refs.mobileDialog.close();
    },
    async pairMobile() {
      this.mobileBusy = true;
      this.mobileError = "";
      try {
        this.mobilePairing = await request("/api/mobile/pair", {
          method: "POST",
          body: JSON.stringify({ store: store() }),
        });
        clearTimeout(expiry);
        expiry = setTimeout(
          () => {
            this.mobilePairing = null;
          },
          Math.max(0, this.mobilePairing.expiresAt - Date.now()),
        );
      } catch (error) {
        this.mobileError = error.message;
      } finally {
        this.mobileBusy = false;
      }
    },
    async revokeMobile(id) {
      if (!confirm("Revoke this phone's access to this memory store?")) return;
      this.mobileError = "";
      try {
        await request("/api/mobile/devices/" + encodeURIComponent(id), {
          method: "DELETE",
        });
        this.mobileStatus = await request(
          "/api/mobile?store=" + encodeURIComponent(store()),
        );
      } catch (error) {
        this.mobileError = error.message;
      }
    },
    async logoutMobile() {
      try {
        await request("/api/logout", { method: "POST" });
        location.reload();
      } catch (error) {
        this.failure = error.message;
      }
    },
  };
}
