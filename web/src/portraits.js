import { fileURL } from "./files.js";

export const portraitURL = (id, picture, revision = "") =>
  fileURL(id, picture.filename, true) +
  "&thumbnail=1&v=" +
  encodeURIComponent(revision);

// Focus is measured in original-image coordinates, independent of preview size.
export function portraitCrop(image, picture) {
  const size = Math.min(image.width, image.height) / picture.zoom;
  return [
    Math.max(
      0,
      Math.min(image.width - size, picture.x * image.width - size / 2),
    ),
    Math.max(
      0,
      Math.min(image.height - size, picture.y * image.height - size / 2),
    ),
    size,
    size,
  ];
}

export function paintPortrait(context, image, picture, x, y, size) {
  context.save();
  context.beginPath();
  context.arc(x + size / 2, y + size / 2, size / 2, 0, 2 * Math.PI);
  context.clip();
  context.drawImage(image, ...portraitCrop(image, picture), x, y, size, size);
  context.restore();
}

export function portraitsUI() {
  let previewImage,
    previewURL = "";
  return {
    pictureError: "",
    entityPicture() {
      return this.selected?.kind === "entities"
        ? this.fields.find((f) => f.key === "profile_picture")
            ?.profile_picture || null
        : null;
    },
    async choosePicture(file) {
      if (this.mode === "source") await this.switchMode("visual");
      if (this.mode !== "visual" || this.inspecting) return;
      this.pictureError = "";
      if (this.entityPicture()?.filename !== file.filename)
        this.changePicture({
          filename: file.filename,
          x: 0.5,
          y: 0.5,
          zoom: 1,
        });
      this.$nextTick(() =>
        this.$refs.editorDialog
          .querySelector(".portrait-editor")
          .scrollIntoView({ block: "nearest", behavior: "smooth" }),
      );
    },
    changePicture(patch) {
      if (this.inspecting || this.mode !== "visual") return;
      let field = this.fields.find((f) => f.key === "profile_picture");
      if (!field) {
        field = {
          key: "profile_picture",
          index: -1,
          type: "profile_picture",
          system: false,
          text: "",
        };
        this.fields.push(field);
        field = this.fields[this.fields.length - 1];
      }
      field.profile_picture = { ...this.entityPicture(), ...patch };
      field.type = "profile_picture";
      field.value = JSON.stringify(field.profile_picture);
      this.metadataChanged();
    },
    clearPicture() {
      this.fields = this.fields.filter((f) => f.key !== "profile_picture");
      this.metadataChanged();
      this.pictureError = "";
    },
    pictureFocus(event) {
      const box = event.currentTarget.getBoundingClientRect();
      this.changePicture({
        x: Math.max(0, Math.min(1, (event.clientX - box.left) / box.width)),
        y: Math.max(0, Math.min(1, (event.clientY - box.top) / box.height)),
      });
    },
    async drawEntityPicture(canvas, picture, id, revision) {
      canvas.width = canvas.height = 256;
      canvas.hidden = true;
      if (!picture || !id) return;
      const url = portraitURL(id, picture, revision);
      if (url !== previewURL) {
        previewURL = url;
        previewImage = new Image();
        previewImage.src = url;
      }
      const image = previewImage;
      try {
        await image.decode();
        if (previewURL !== url || this.selected?.id !== id) return;
        const current = this.entityPicture();
        if (!current) return;
        paintPortrait(canvas.getContext("2d"), image, current, 0, 0, 256);
        canvas.hidden = false;
        this.pictureError = "";
      } catch {
        if (previewURL === url && this.selected?.id === id) {
          previewURL = "";
          this.pictureError =
            "This image could not be displayed. Choose another picture or retry after reopening the record.";
        }
      }
    },
    portraitURL,
  };
}
