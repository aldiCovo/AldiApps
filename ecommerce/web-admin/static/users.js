"use strict";

// Accounts and their role. Uses $, el, button, api, send, when, handleAuth and currentAdmin
// from admin.js. user-service keeps at least one admin, and admin-service refuses a change
// to the admin's own role.

const userPage = 50;
let userRole = "";
let userSearch = "";
let users = [];
let usersToken = 0;

const userMessages = {
  "the last admin can't stop being an admin": "Harus ada setidaknya satu admin. Jadikan akun lain admin dulu.",
  "you can't change your own role": "Role akunmu sendiri tidak bisa diubah dari sini.",
  "user not found": "Akun itu sudah tidak ada. Daftar dimuat ulang.",
};

async function loadUsers(reset) {
  const token = ++usersToken;
  if (reset) {
    users = [];
    $("user-list").replaceChildren();
  }
  $("users-status").textContent = "Memuat pengguna…";
  $("more-users").hidden = true;
  const query = new URLSearchParams({ limit: String(userPage), offset: String(users.length) });
  if (userSearch) query.set("q", userSearch);
  if (userRole) query.set("role", userRole);
  try {
    const page = await api(`/users?${query}`);
    if (token !== usersToken) return;
    users = users.concat(page.users);
    $("user-list").append(...page.users.map(userRow));
    $("more-users").hidden = users.length >= page.total;
    $("users-status").textContent = page.total === 0 ? "Tidak ada pengguna yang cocok." : `${page.total.toLocaleString("id-ID")} pengguna`;
  } catch (error) {
    if (token !== usersToken || handleAuth(error)) return;
    $("users-status").textContent = "Pengguna belum bisa dimuat. Coba lagi sebentar lagi.";
  }
}

function userRow(user) {
  const row = el("article", "user-row");
  row.dataset.user = user.id;
  const who = el("div", "user-who");
  who.append(el("strong", "", user.name), el("span", "muted", user.email));
  const facts = el("div", "user-facts");
  facts.append(
    el("span", `state ${user.role === "admin" ? "todo" : ""}`, user.role === "admin" ? "Admin" : "Pelanggan"),
    el("span", "muted small", `Bergabung ${when(user.created_at)}`),
  );
  const actions = el("div", "user-actions");
  const status = el("p", "status user-status", "");
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");

  if (currentAdmin && user.id === currentAdmin.id) {
    actions.append(el("span", "muted small", "Akun kamu"));
  } else {
    const promote = user.role !== "admin";
    const open = button(promote ? "Jadikan admin" : "Cabut admin", promote ? "secondary small-button" : "link danger", () => {
      open.hidden = true;
      const ask = el("div", "confirm");
      ask.append(el("p", "", promote
        ? `Jadikan ${user.name} admin? Akun ini bisa masuk ke halaman admin dan mengubah pesanan, produk, foto, dan role pengguna lain.`
        : `Cabut akses admin ${user.name}? Akun ini langsung keluar dari halaman admin, tapi tetap bisa belanja.`));
      const buttons = el("div", "form-actions");
      const yes = button(promote ? "Ya, jadikan admin" : "Ya, cabut admin", promote ? "primary" : "primary danger-fill", () => changeRole(user, promote ? "admin" : "customer", row, status));
      const no = button("Batal", "link", () => {
        ask.remove();
        open.hidden = false;
        open.focus();
      });
      buttons.append(yes, no);
      ask.append(buttons);
      actions.append(ask);
      yes.focus();
    });
    actions.append(open);
  }
  row.append(who, facts, actions, status);
  return row;
}

async function changeRole(user, role, row, status) {
  setBusy(row, true);
  try {
    const updated = await send("PUT", `/users/${user.id}/role`, { role });
    const done = role === "admin" ? `${updated.name} sekarang admin.` : `${updated.name} bukan admin lagi.`;
    if (userRole && updated.role !== userRole) {
      // It now belongs to the other tab.
      users = users.filter((item) => item.id !== user.id);
      row.remove();
      $("users-status").textContent = done;
      return;
    }
    users = users.map((item) => (item.id === user.id ? updated : item));
    const fresh = userRow(updated);
    fresh.querySelector(".user-status").textContent = done;
    row.replaceWith(fresh);
  } catch (error) {
    if (handleAuth(error)) return;
    const message = userMessages[error.message] || (error.status === 404 ? userMessages["user not found"] : "Role belum bisa diubah. Coba lagi sebentar lagi.");
    if (error.status === 404) {
      loadUsers(true).then(() => {
        $("users-status").textContent = message;
      });
      return;
    }
    const fresh = userRow(user);
    fresh.querySelector(".user-status").textContent = message;
    row.replaceWith(fresh);
  }
}

$("user-search").addEventListener("submit", (event) => {
  event.preventDefault();
  userSearch = $("user-q").value.trim();
  loadUsers(true);
});
for (const tab of document.querySelectorAll(".user-tab")) {
  tab.addEventListener("click", () => {
    userRole = tab.dataset.role;
    for (const other of document.querySelectorAll(".user-tab")) other.setAttribute("aria-pressed", String(other === tab));
    loadUsers(true);
  });
}
$("more-users").addEventListener("click", () => loadUsers(false));
