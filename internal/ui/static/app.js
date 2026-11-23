// Demo UI for the gateway. Everything the server returns is written with
// textContent, never innerHTML, and the token lives only in sessionStorage.
const TOKEN_KEY = "forge.token";

const $ = (id) => document.getElementById(id);

function storedToken() {
  try {
    return sessionStorage.getItem(TOKEN_KEY);
  } catch {
    return null;
  }
}

function saveToken(token) {
  try {
    sessionStorage.setItem(TOKEN_KEY, token);
  } catch {
    // Private mode can block storage; the session then lasts until reload.
  }
}

function clearToken() {
  try {
    sessionStorage.removeItem(TOKEN_KEY);
  } catch {
    // Nothing to clear.
  }
}

class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

// api calls the gateway on this origin. The path must start with /api/v3.
async function api(path, token) {
  let response;
  try {
    response = await fetch(path, {
      headers: { Authorization: `token ${token}`, Accept: "application/json" },
      // Same-origin only: a private Codespaces port needs its tunnel cookie.
      // The gateway itself never sets or reads cookies.
      credentials: "same-origin",
      cache: "no-store",
    });
  } catch {
    throw new ApiError(0, "Could not reach the gateway.");
  }
  if (!response.ok) {
    let message = response.statusText;
    try {
      message = (await response.json()).message || message;
    } catch {
      // Keep the status text.
    }
    throw new ApiError(response.status, message);
  }
  return response.json();
}

function httpUrl(value) {
  try {
    const url = new URL(value);
    return url.protocol === "http:" || url.protocol === "https:" ? url.href : null;
  } catch {
    return null;
  }
}

function show(view) {
  $("login").hidden = view !== "login";
  $("profile").hidden = view !== "profile";
  $("signout").hidden = view !== "profile";
}

function setStatus(text) {
  $("status").textContent = text;
  $("status").hidden = text === "";
}

function fact(label, value) {
  const dt = document.createElement("dt");
  dt.textContent = label;
  const dd = document.createElement("dd");
  dd.textContent = value;
  return [dt, dd];
}

function renderProfile(user) {
  $("display-name").textContent = user.name || user.login;

  const login = $("login-name");
  login.replaceChildren();
  const profileUrl = httpUrl(user.html_url);
  if (profileUrl) {
    const link = document.createElement("a");
    link.href = profileUrl;
    link.textContent = user.login;
    link.rel = "noopener noreferrer";
    login.append(link);
  } else {
    login.textContent = user.login;
  }

  $("bio").textContent = user.bio || "";
  $("bio").hidden = !user.bio;

  const avatar = $("avatar");
  const avatarUrl = httpUrl(user.avatar_url);
  avatar.hidden = !avatarUrl;
  if (avatarUrl) {
    avatar.src = avatarUrl;
    avatar.referrerPolicy = "no-referrer";
  }

  const rows = [
    ["Public repositories", user.public_repos],
    ["Private repositories", user.total_private_repos],
    ["Followers", user.followers],
    ["Following", user.following],
    ["Location", user.location],
    ["Email", user.email],
    ["Joined", user.created_at ? new Date(user.created_at).toLocaleDateString() : null],
  ];
  const facts = $("facts");
  facts.replaceChildren();
  for (const [label, value] of rows) {
    // null means the token's scopes (or Gitea) cannot supply the value.
    if (value !== null && value !== undefined) {
      facts.append(...fact(label, String(value)));
    }
  }
}

function showLoginError(text) {
  $("login-error").textContent = text;
  $("login-error").hidden = text === "";
}

async function signIn(token) {
  setStatus("Signing in…");
  try {
    const user = await api("/api/v3/user", token);
    saveToken(token);
    renderProfile(user);
    show("profile");
    showLoginError("");
    setStatus("");
    return true;
  } catch (error) {
    clearToken();
    show("login");
    setStatus("");
    showLoginError(error.status === 401 ? "Bad credentials" : error.message);
    return false;
  }
}

function signOut() {
  clearToken();
  $("token").value = "";
  $("facts").replaceChildren();
  show("login");
}

// Routes are hash-based so later slices can add pages (#/repos, ...) here.
const routes = { "": "profile" };

function route() {
  if (!(location.hash.replace(/^#\/?/, "") in routes)) {
    location.hash = "#/";
  }
}

$("login-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const token = $("token").value.trim();
  if (token === "") {
    return;
  }
  const ok = await signIn(token);
  if (ok) {
    $("token").value = "";
  }
});
$("signout").addEventListener("click", signOut);
window.addEventListener("hashchange", route);

route();
const existing = storedToken();
if (existing) {
  signIn(existing);
} else {
  show("login");
}
