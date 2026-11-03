"use strict";

// Product photos (up to four; the first is the one on the shop's product card) and category
// photos. Uses $, el, button, api, send and handleAuth from admin.js. The browser makes each
// photo a JPEG of at most 1600 pixels before sending it, which also drops its location data.

const photoSlots = 4;
const maxPhotoSide = 1600;
const maxSourceBytes = 25 * 1024 * 1024;

let productPhotos = {}; // product id -> four URLs, "" where empty
let categoryPhotos = {}; // category id -> URL
let photosLoaded = false;

const photoMessages = {
  "the file is not a JPEG, PNG or WEBP photo": "File ini bukan foto JPG, PNG, atau WEBP.",
  "this category name can't name a photo; use letters, numbers and spaces": "Nama kategori ini hanya boleh berisi huruf, angka, dan spasi supaya bisa diberi foto. Ubah namanya dulu.",
  "a photo can be at most 5 MB": "Foto terlalu besar. Pilih foto lain.",
  "photo not found": "Foto itu sudah tidak ada. Muat ulang halaman.",
  "category not found": "Kategori itu sudah tidak ada. Muat ulang halaman.",
};

class PhotoProblem extends Error {}

// Photos failing to load mustn't hide the products, so this never throws.
async function loadPhotoLists() {
  try {
    const [products, categories] = await Promise.all([api("/photos/products"), api("/photos/categories")]);
    productPhotos = products.photos || {};
    categoryPhotos = categories.photos || {};
    photosLoaded = true;
  } catch {
    productPhotos = {};
    categoryPhotos = {};
    photosLoaded = false;
  }
}

function photoError(error, fallback) {
  if (error instanceof PhotoProblem) return error.message;
  if (handleAuth(error)) return null;
  if (photoMessages[error.message]) return photoMessages[error.message];
  if (error.status === 404) return "Data ini sudah tidak ada. Muat ulang halaman.";
  if (error.status === 413) return photoMessages["a photo can be at most 5 MB"];
  return fallback;
}

async function preparePhoto(file) {
  if (!file.type.startsWith("image/")) throw new PhotoProblem("Pilih file foto, seperti JPG atau PNG.");
  if (file.size > maxSourceBytes) throw new PhotoProblem("Foto terlalu besar. Pilih foto di bawah 25 MB.");
  let bitmap;
  try {
    bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
  } catch {
    throw new PhotoProblem("Foto ini tidak bisa dibaca browser. Coba foto JPG atau PNG.");
  }
  const scale = Math.min(1, maxPhotoSide / Math.max(bitmap.width, bitmap.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(bitmap.width * scale));
  canvas.height = Math.max(1, Math.round(bitmap.height * scale));
  const context = canvas.getContext("2d");
  // A transparent PNG gets white behind it, as JPEG has no transparency.
  context.fillStyle = "#fff";
  context.fillRect(0, 0, canvas.width, canvas.height);
  context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  bitmap.close();
  const blob = await new Promise((resolve) => canvas.toBlob(resolve, "image/jpeg", 0.86));
  if (!blob) throw new PhotoProblem("Foto ini tidak bisa diproses. Coba foto lain.");
  return blob;
}

function uploadPhoto(path, blob) {
  return api(path, { method: "PUT", headers: { "Content-Type": "image/jpeg" }, body: blob });
}

// A button that opens the file picker, as a bare file input can't be styled.
function photoPicker(label, className, onFile) {
  const input = el("input");
  input.type = "file";
  input.accept = "image/*";
  input.hidden = true;
  const open = button(label, className, () => input.click());
  input.addEventListener("change", () => {
    const file = input.files && input.files[0];
    input.value = "";
    if (file) onFile(file);
  });
  const wrap = el("span", "photo-picker");
  wrap.append(open, input);
  return wrap;
}

function photoSection(product, status) {
  const section = el("section", "photos");
  section.setAttribute("aria-label", `Foto ${product.name}`);
  const head = el("div", "photos-head");
  head.append(el("h3", "", "Foto"), el("p", "muted small", "Foto 1 tampil di kartu produk toko. Foto tegak 4:5 terlihat paling baik."));
  section.append(head);
  if (!photosLoaded) {
    section.append(el("p", "muted small", "Foto belum bisa dimuat. Muat ulang halaman untuk mengubah foto."));
    return section;
  }
  const photos = productPhotos[product.id] || Array(photoSlots).fill("");
  const grid = el("div", "photo-grid");

  const run = async (action, busyNote, done) => {
    setBusy(section, true);
    status.textContent = busyNote;
    try {
      const answer = await action();
      productPhotos[product.id] = answer.photos;
      section.replaceWith(photoSection(product, status));
      status.textContent = done;
    } catch (error) {
      const message = photoError(error, "Foto belum bisa diubah. Coba lagi sebentar lagi.");
      if (message === null) return;
      status.textContent = message;
      setBusy(section, false);
    }
  };

  photos.forEach((url, index) => {
    const slot = index + 1;
    const figure = el("figure", `photo-slot${url ? " filled" : ""}`);
    figure.dataset.slot = String(slot);
    const frame = el("div", "photo-frame");
    if (url) {
      const img = el("img");
      img.src = url;
      img.alt = `${product.name}, foto ${slot}`;
      img.loading = "lazy";
      frame.append(img);
    } else {
      frame.append(el("span", "", "Kosong"));
    }
    figure.append(frame, el("figcaption", "", slot === 1 ? "Foto 1 · utama" : `Foto ${slot}`));

    const tools = el("div", "photo-tools");
    tools.append(photoPicker(url ? "Ganti" : "Pilih foto", "secondary small-button", (file) => run(async () => {
      const blob = await preparePhoto(file);
      return uploadPhoto(`/photos/products/${product.id}/${slot}`, blob);
    }, `Mengunggah foto ${slot}…`, `Foto ${slot} disimpan.`)));
    if (url && slot > 1) {
      tools.append(button("Jadikan utama", "link", () => run(
        () => send("POST", `/photos/products/${product.id}/${slot}/first`),
        "Mengubah urutan foto…", `Foto ${slot} sekarang foto utama.`)));
    }
    if (url) {
      const remove = button("Hapus", "link danger", () => {
        if (remove.dataset.confirm !== "yes") {
          remove.dataset.confirm = "yes";
          remove.textContent = "Yakin hapus?";
          return;
        }
        run(() => send("DELETE", `/photos/products/${product.id}/${slot}`), `Menghapus foto ${slot}…`, `Foto ${slot} dihapus.`);
      });
      tools.append(remove);
    }
    figure.append(tools);
    grid.append(figure);
  });
  section.append(grid);
  return section;
}

// The thumbnail and photo buttons on a category's row in "Kelola kategori".
function categoryPhotoTools(category) {
  const box = el("div", "category-photo");
  if (!photosLoaded) return box;
  const url = categoryPhotos[category.id];
  if (url) {
    const img = el("img", "category-thumb");
    img.src = url;
    img.alt = `Foto kategori ${category.name}`;
    box.append(img);
  } else {
    box.append(el("span", "category-thumb empty", "Tanpa foto"));
  }

  const run = async (action, busyNote, done) => {
    setBusy(box, true);
    $("categories-status").textContent = busyNote;
    try {
      const answer = await action();
      if (answer && answer.photo) categoryPhotos[category.id] = answer.photo;
      else delete categoryPhotos[category.id];
      box.replaceWith(categoryPhotoTools(category));
      $("categories-status").textContent = done;
    } catch (error) {
      const message = photoError(error, "Foto kategori belum bisa diubah. Coba lagi sebentar lagi.");
      if (message === null) return;
      $("categories-status").textContent = message;
      setBusy(box, false);
    }
  };

  box.append(photoPicker(url ? "Ganti foto" : "Pilih foto", "secondary small-button", (file) => run(async () => {
    const blob = await preparePhoto(file);
    return uploadPhoto(`/photos/categories/${category.id}`, blob);
  }, `Mengunggah foto ${category.name}…`, `Foto kategori ${category.name} disimpan.`)));
  if (url) {
    const remove = button("Hapus foto", "link danger", () => {
      if (remove.dataset.confirm !== "yes") {
        remove.dataset.confirm = "yes";
        remove.textContent = "Yakin hapus foto?";
        return;
      }
      run(() => send("DELETE", `/photos/categories/${category.id}`), "Menghapus foto…", `Foto kategori ${category.name} dihapus.`);
    });
    box.append(remove);
  }
  return box;
}
