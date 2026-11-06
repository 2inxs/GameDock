"use strict";

const get = (id) => document.getElementById(id);
const stateLabels = {
  stopped: "Остановлен",
  starting: "Запускается…",
  unreachable: "Сервер не ответил",
  running: "Работает",
  stopping: "Останавливается…",
  failed: "Ошибка сервера",
};
const feedback = get("feedback");
const logs = get("logs");
const select = get("backup-select");
const dialog = get("restore-dialog");
const forceDialog = get("force-dialog");
let status;
let connected = false;
let busy = false;
let pendingStatus;
let backupIDs = [];

function api() {
  return window.go?.main?.App;
}

function setText(element, value) {
  if (element.textContent !== value) element.textContent = value;
}

function showFeedback(message, error = false) {
  setText(feedback, message);
  feedback.dataset.error = String(error);
  feedback.hidden = !message;
}

function updateControls() {
  const locked = busy || !connected || status?.busy;
  const ready = Boolean(status?.ready);
  const idle = status?.state === "stopped" || status?.state === "failed";
  const active = ["starting", "running", "unreachable"].includes(status?.state);
  const stopping = status?.state === "stopping";
  const setupDisabled = locked || !idle || !get("eula").checked;
  get("setup-download").disabled = setupDisabled;
  get("setup-import").disabled = setupDisabled;
  get("eula").disabled = busy;
  get("eula-link").disabled = !connected || busy;
  get("start").disabled = locked || !ready || !idle;
  setText(get("stop"), stopping ? "Ждать ещё" : "Остановить");
  get("stop").disabled = locked || (!active && !stopping);
  get("force-stop").hidden = !stopping;
  get("force-stop").disabled = locked || !stopping;
  get("java").disabled = locked || !idle;
  get("memory").disabled = locked || !idle;
  get("pick-java").disabled = locked || !idle;
  get("backup").disabled = locked || !ready || !idle;
  select.disabled = locked || !ready || !idle || backupIDs.length === 0;
  get("restore").disabled = select.disabled || !select.value;
}

function updateBackups(backups) {
  if (backups.length === backupIDs.length && backups.every((id, index) => id === backupIDs[index])) return;
  const selected = select.value;
  const placeholder = new Option(backups.length ? "Выберите копию" : "Копий пока нет", "");
  select.replaceChildren(placeholder, ...backups.map((id) => new Option(id, id)));
  select.value = backups.includes(selected) ? selected : "";
  backupIDs = backups;
}

function renderStatus(next) {
  const ready = Boolean(next.ready);
  const active = ["starting", "running", "unreachable", "stopping"].includes(next.state);
  const showServer = ready || active;
  const connection = get("connection");
  connection.dataset.state = showServer ? next.state : "setup";
  setText(connection, showServer ? (stateLabels[next.state] || "Состояние неизвестно") : "Требуется настройка");
  get("setup").hidden = showServer;
  get("server").hidden = !showServer;
  get("monitor").hidden = !showServer;
  setText(get("version"), next.version ? `Vanilla ${next.version}` : "Vanilla");
  const notice = get("notice");
  setText(notice, next.notice || "");
  notice.hidden = !next.notice;
  const logText = next.lines?.join("\n") || "Журнал пока пуст.";
  if (logs.textContent !== logText) {
    logs.textContent = logText;
    if (get("follow").checked) logs.scrollTop = logs.scrollHeight;
  }
  updateBackups(next.backups || []);
}

async function readStatus() {
  try {
    const app = api();
    if (!app) throw new Error("Связь с GameDock недоступна.");
    status = await app.Status();
    renderStatus(status);
    connected = true;
  } catch (error) {
    connected = false;
    const connection = get("connection");
    connection.dataset.state = "error";
    setText(connection, `Ошибка связи: ${error?.message || error}`);
  }
  updateControls();
}

function refresh() {
  if (!pendingStatus) {
    pendingStatus = readStatus().finally(() => { pendingStatus = undefined; });
  }
  return pendingStatus;
}

async function run(progress, action) {
  if (busy || !connected) return;
  busy = true;
  showFeedback(progress);
  updateControls();
  try {
    showFeedback(await action(api()) || "");
  } catch (error) {
    showFeedback(error?.message || String(error), true);
  } finally {
    await pendingStatus;
    await refresh();
    busy = false;
    updateControls();
  }
}

get("eula").addEventListener("change", updateControls);
select.addEventListener("change", updateControls);
get("follow").addEventListener("change", (event) => {
  if (event.target.checked) logs.scrollTop = logs.scrollHeight;
});

get("eula-link").addEventListener("click", () => run("", (app) => app.OpenEULA()));
get("setup-download").addEventListener("click", () => run("Загружаю сервер…", async (app) => {
  await app.Setup(get("eula").checked);
  return "Сервер загружен и готов к запуску.";
}));
get("setup-import").addEventListener("click", () => run("Выберите папку исходного сервера.", async (app) => {
  const source = await app.PickFolder();
  if (!source) return;
  showFeedback("Импортирую сервер…");
  await app.Import(source, get("eula").checked);
  return "Сервер импортирован и готов к запуску.";
}));
get("pick-java").addEventListener("click", () => run("Выберите java.exe из Java 21.", async (app) => {
  const path = await app.PickJava();
  if (path) get("java").value = path;
}));
get("start").addEventListener("click", () => {
  const memory = get("memory");
  if (!memory.reportValidity() || !Number.isSafeInteger(memory.valueAsNumber)) {
    showFeedback("Укажите целое число от 1024 до 8192 МиБ.", true);
    return;
  }
  run("Отправляю команду запуска…", async (app) => {
    await app.Start(get("java").value.trim(), memory.valueAsNumber);
  });
});
get("stop").addEventListener("click", () => run("Отправляю команду остановки…", async (app) => {
  await app.Stop();
}));
get("force-stop").addEventListener("click", () => {
  forceDialog.returnValue = "cancel";
  forceDialog.showModal();
});
forceDialog.addEventListener("close", () => {
  if (forceDialog.returnValue !== "confirm" || get("force-stop").disabled) return;
  run("Завершаю сервер…", (app) => app.ForceStop());
});
get("backup").addEventListener("click", () => run("Создаю резервную копию…", async (app) => {
  const id = await app.Backup();
  return `Создана копия ${id}.`;
}));
get("restore").addEventListener("click", () => {
  setText(get("restore-name"), select.value);
  dialog.returnValue = "cancel";
  dialog.showModal();
});
dialog.addEventListener("close", () => {
  if (dialog.returnValue !== "confirm" || get("restore").disabled) return;
  const id = get("restore-name").textContent;
  if (!backupIDs.includes(id)) return;
  run("Восстанавливаю данные…", async (app) => {
    await app.Restore(id);
    return `Данные восстановлены из копии ${id}.`;
  });
});

refresh();
window.setInterval(refresh, 1500);
