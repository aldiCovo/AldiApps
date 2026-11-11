"use strict";

// Shop the look: a photo of an outfit with a numbered point on each product worn. The shop's
// homepage shows the most recently changed look that's turned on and has a photo and products.
// Uses $, el, button, api, send, handleAuth from admin.js, field, textInput, setBusy from
// products.js, and photoPicker, preparePhoto, uploadPhoto, photoError from photos.js.

const maxLookItems = 6;
let looks = [];
let lookProducts = [];
let looksToken = 0;

const lookMessages = {
  "at most 20 looks can be kept": "Paling banyak 20 look. Hapus yang lama dulu.",
  "look not found": "Look itu sudah tidak ada. Daftar dimuat ulang.",
  "product not found": "Salah satu produk sudah dihapus. Pilih produk lain.",
};

function lookError(error, fallback) {
  const message = photoError(error, fallback);
  if (message === null) return null;
  return lookMessages[error.message] || message;
}

function lookState(look) {
  if (!look.active) return ["Nonaktif", ""];
  if (!look.photo) return ["Belum ada foto", "bad"];
  if (look.items.length === 0) return ["Belum ada produk", "bad"];
  return ["Aktif", "good"];
}

async function loadLooks(note = "") {
  const token = ++looksToken;
  $("looks-status").textContent = "Memuat look…";
  try {
    const [answer, products] = await Promise.all([api("/looks"), api("/products?limit=100")]);
    if (token !== looksToken) return;
    looks = answer.looks;
    lookProducts = products.products;
    renderLooks(note);
  } catch (error) {
    if (token !== looksToken || handleAuth(error)) return;
    $("looks-status").textContent = "Look belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

// The shop shows the newest active complete look, so that one is marked.
function shownLookId() {
  const ready = looks.filter((look) => lookState(look)[1] === "good");
  ready.sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  return ready.length > 0 ? ready[0].id : null;
}

function renderLooks(note = "") {
  const shown = shownLookId();
  $("look-list").replaceChildren(...looks.map((look) => lookRow(look, look.id === shown)));
  $("looks-status").textContent = note || (looks.length === 0 ? "Belum ada look." : shown === null ? "Belum ada look yang tampil di beranda." : "");
}

function lookRow(look, shown) {
  const row = el("article", "look-row");
  const thumb = el("div", "look-thumb");
  if (look.photo_url) {
    const img = el("img");
    img.src = look.photo_url;
    img.alt = "";
    thumb.append(img);
  }
  const who = el("div", "voucher-who");
  who.append(el("strong", "", [look.title, look.accent].filter(Boolean).join(", ")));
  if (look.eyebrow) who.append(el("span", "muted small", look.eyebrow));
  who.append(el("span", "muted small", `${look.items.length} produk`));
  const [label, tone] = lookState(look);
  const facts = el("div", "voucher-facts");
  facts.append(el("span", `state ${tone}`, label));
  if (shown) facts.append(el("span", "small banner-flag", "Tampil di beranda"));
  const actions = el("div", "voucher-actions");
  const status = el("p", "status", "");
  actions.append(button("Ubah", "link", () => {
    const editor = lookEditor(look);
    row.replaceWith(editor);
    editor.querySelector("input").focus();
  }));
  const remove = button("Hapus", "link danger", async () => {
    if (remove.dataset.confirm !== "yes") {
      remove.dataset.confirm = "yes";
      remove.textContent = "Yakin hapus look ini?";
      return;
    }
    setBusy(row, true);
    try {
      await send("DELETE", `/looks/${look.id}`);
      looks = looks.filter((item) => item.id !== look.id);
      renderLooks("Look dihapus.");
    } catch (error) {
      const message = lookError(error, "Look belum bisa dihapus. Coba lagi sebentar lagi.");
      if (message === null) return;
      if (error.status === 404) loadLooks(message);
      else {
        status.textContent = message;
        setBusy(row, false);
      }
    }
  });
  actions.append(remove);
  row.append(thumb, who, facts, actions, status);
  return row;
}

function productLabel(product) {
  return `${product.name} · ${rupiah.format(product.price)}`;
}

// The editor: text, the photo, and the products with their points. A new look is saved
// first, as the photo needs its id.
function lookEditor(look) {
  const form = el("form", "product-form look-form panel");
  form.noValidate = true;
  const eyebrow = textInput(look ? look.eyebrow : "Shop the look", 60);
  const title = textInput(look ? look.title : "", 80);
  const accent = textInput(look ? look.accent : "", 80);
  const active = el("input");
  active.type = "checkbox";
  active.checked = look ? look.active : true;
  const activeField = el("label", "check");
  activeField.append(active, document.createTextNode(" Tampilkan di beranda"));
  const error = el("p", "error", "");
  error.setAttribute("role", "alert");
  error.hidden = true;
  const note = el("p", "status", "");
  note.setAttribute("role", "status");

  let items = look ? look.items.map((item) => ({ ...item })) : [];
  let chosen = 0;
  let photoURL = look ? look.photo_url : "";

  const stage = el("div", "look-stage");
  const hint = el("p", "muted small", "");
  const list = el("ol", "look-items");
  const add = button("+ Tambah produk", "link", () => {
    const used = new Set(items.map((item) => item.product_id));
    const next = lookProducts.find((product) => !used.has(product.id));
    if (!next) return;
    items.push({ product_id: next.id, x: 50, y: 50 });
    chosen = items.length - 1;
    draw();
  });

  function draw() {
    stage.replaceChildren();
    if (photoURL) {
      const img = el("img");
      img.src = photoURL;
      img.alt = "Foto look";
      stage.append(img);
      items.forEach((item, i) => {
        const dot = el("span", `look-dot${i === chosen ? " chosen" : ""}`, String(i + 1));
        dot.style.left = `${item.x}%`;
        dot.style.top = `${item.y}%`;
        stage.append(dot);
      });
    } else {
      stage.append(el("p", "muted small look-empty", look ? "Belum ada foto." : "Simpan dulu, lalu pilih fotonya."));
    }
    hint.textContent = photoURL && items.length > 0
      ? `Klik foto untuk memindahkan titik ${chosen + 1}. Pilih nomor di daftar untuk titik lain.`
      : "";
    list.replaceChildren(...items.map((item, i) => {
      const li = el("li", `look-item${i === chosen ? " chosen" : ""}`);
      const pick = button(String(i + 1), "look-number", () => {
        chosen = i;
        draw();
      });
      pick.setAttribute("aria-pressed", String(i === chosen));
      pick.setAttribute("aria-label", `Pilih titik ${i + 1}`);
      const select = el("select");
      select.setAttribute("aria-label", `Produk titik ${i + 1}`);
      const taken = new Set(items.filter((_, j) => j !== i).map((other) => other.product_id));
      for (const product of lookProducts) {
        if (!taken.has(product.id)) select.append(new Option(productLabel(product), product.id));
      }
      if (!lookProducts.some((product) => product.id === item.product_id)) {
        select.prepend(new Option("Produk sudah dihapus", item.product_id));
      }
      select.value = String(item.product_id);
      select.addEventListener("change", () => {
        item.product_id = Number(select.value);
        draw();
      });
      const remove = button("Hapus", "link danger", () => {
        items.splice(i, 1);
        chosen = Math.max(0, Math.min(chosen, items.length - 1));
        draw();
      });
      li.append(pick, select, remove);
      return li;
    }));
    add.hidden = !look || items.length >= maxLookItems || items.length >= lookProducts.length;
  }

  stage.addEventListener("click", (event) => {
    const img = stage.querySelector("img");
    if (!img || items.length === 0) return;
    const box = img.getBoundingClientRect();
    const x = ((event.clientX - box.left) / box.width) * 100;
    const y = ((event.clientY - box.top) / box.height) * 100;
    if (x < 0 || x > 100 || y < 0 || y > 100) return;
    items[chosen].x = Math.round(x * 10) / 10;
    items[chosen].y = Math.round(y * 10) / 10;
    draw();
  });

  const picker = photoPicker(look && look.photo ? "Ganti foto" : "Pilih foto", "secondary", async (file) => {
    error.hidden = true;
    note.textContent = "Mengunggah foto…";
    setBusy(form, true);
    try {
      const blob = await preparePhoto(file);
      const saved = await uploadPhoto(`/looks/${look.id}/photo`, blob);
      look.photo = saved.photo;
      photoURL = saved.photo_url;
      note.textContent = "Foto disimpan.";
      draw();
    } catch (failure) {
      const message = lookError(failure, "Foto belum bisa disimpan. Coba lagi sebentar lagi.");
      note.textContent = "";
      if (message !== null) {
        error.textContent = message;
        error.hidden = false;
      }
    }
    setBusy(form, false);
  });
  picker.hidden = !look;

  const save = el("button", "primary", look ? "Simpan look" : "Simpan dan lanjut ke foto");
  save.type = "submit";
  const actions = el("div", "form-actions");
  actions.append(save, button("Batal", "link", () => renderLooks()));

  const media = el("div", "look-media");
  media.append(stage, picker, hint);
  const products = el("div", "look-products");
  products.append(el("h3", "", "Produk di foto"), list, add);
  if (!look) products.hidden = true;
  const grid = el("div", "voucher-grid");
  grid.append(field("Teks kecil di atas judul", eyebrow), field("Judul", title), field("Lanjutan judul, miring (opsional)", accent));
  form.append(el("h2", "", look ? "Ubah look" : "Look baru"), grid, activeField, media, products, note, error, actions);

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (title.value.trim() === "") {
      error.textContent = "Isi judulnya.";
      error.hidden = false;
      title.focus();
      return;
    }
    error.hidden = true;
    const body = {
      eyebrow: eyebrow.value.trim(), title: title.value.trim(), accent: accent.value.trim(),
      active: active.checked, items,
    };
    setBusy(form, true);
    try {
      if (look) {
        await send("PUT", `/looks/${look.id}`, body);
        await loadLooks("Look disimpan.");
      } else {
        const saved = await post("/looks", body);
        looks = [...looks, saved];
        const editor = lookEditor(saved);
        editor.querySelector(".status").textContent = "Look dibuat. Sekarang pilih fotonya, lalu tambahkan produk.";
        form.replaceWith(editor);
      }
    } catch (failure) {
      const message = lookError(failure, "Look belum bisa disimpan. Coba lagi sebentar lagi.");
      if (message === null) return;
      if (failure.status === 404 && failure.message === "look not found") {
        loadLooks(message);
        return;
      }
      error.textContent = message;
      error.hidden = false;
      setBusy(form, false);
    }
  });
  draw();
  return form;
}

$("new-look").addEventListener("click", () => {
  const form = lookEditor(null);
  $("look-list").prepend(form);
  form.querySelector("input").focus();
});
