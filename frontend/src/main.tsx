import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity,
  ArrowDownLeft,
  Box,
  Check,
  ChevronDown,
  CircleHelp,
  Command,
  Folder,
  FolderOpen,
  HardDrive,
  LayoutGrid,
  MoreHorizontal,
  Network,
  Play,
  Plus,
  Power,
  RefreshCw,
  Save,
  Server,
  Settings as SettingsIcon,
  Square,
  Terminal,
  Trash2,
  X,
  Zap,
} from "lucide-react";
import "./style.css";
import Rooms, { type RoomAPI } from "./Rooms";

type Config = {
  id: string;
  name: string;
  game: string;
  javaPath: string;
  jarPath: string;
  javaArgsFile: string;
  filesDir: string;
  workingDir: string;
  minMemoryMB: number;
  maxMemoryMB: number;
  createdAt: string;
  updatedAt: string;
};
type Runtime = {
  serverId: string;
  runId: string;
  state: string;
  pid: number;
  error: string;
  startedAt?: string;
};
type View = {
  config: Config;
  runtime: Runtime;
  available: boolean;
  availabilityError: string;
};
type Log = {
  serverId: string;
  runId: string;
  sequence: number;
  time: string;
  stream: string;
  message: string;
};
type Settings = { libraryDir: string };
type AppInfo = {
  version: string;
  dataDir: string;
  error: string;
  edition: "full" | "client";
  capabilities: { servers: boolean; rooms: boolean; hosting: boolean };
};
type Bridge = {
  ListServers: () => Promise<View[]>;
  SaveServer: (c: Config) => Promise<Config>;
  DeleteServer: (id: string) => Promise<void>;
  StartServer: (id: string) => Promise<Runtime>;
  StopServer: (id: string) => Promise<void>;
  KillServer: (id: string) => Promise<void>;
  SendCommand: (id: string, c: string) => Promise<void>;
  GetSettings: () => Promise<Settings>;
  SaveSettings: (s: Settings) => Promise<Settings>;
  PickDirectory: (title: string, current: string) => Promise<string>;
  PickJava: (current: string) => Promise<string>;
  PickJar: (current: string) => Promise<string>;
  PickArgsFile: (current: string) => Promise<string>;
  GetLogs: (id: string, after: number) => Promise<Log[]>;
  GetRuntime: (id: string) => Promise<Runtime>;
  GetAppInfo: () => Promise<AppInfo>;
};
declare global {
  interface Window {
    go?: { main?: { App?: Bridge } };
    runtime?: {
      EventsOn: (name: string, cb: (event: any) => void) => () => void;
    };
  }
}
const api = window.go?.main?.App as Bridge;
const empty = (): Config => ({
  id: "",
  name: "",
  game: "minecraft",
  javaPath: "",
  jarPath: "",
  javaArgsFile: "",
  filesDir: "",
  workingDir: "",
  minMemoryMB: 1024,
  maxMemoryMB: 4096,
  createdAt: "0001-01-01T00:00:00Z",
  updatedAt: "0001-01-01T00:00:00Z",
});
const states: Record<string, string> = {
  stopped: "Остановлен",
  starting: "Запускается",
  running: "Процесс запущен",
  stopping: "Останавливается",
  failed: "Ошибка",
};
const active = (r?: Runtime) =>
  !!r && ["starting", "running", "stopping"].includes(r.state);
const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

function App() {
  const [views, setViews] = useState<View[]>([]),
    [selected, setSelected] = useState(""),
    [page, setPage] = useState<"servers" | "storage" | "rooms">("rooms");
  const [settings, setSettings] = useState<Settings>({ libraryDir: "" }),
    [savedLibrary, setSavedLibrary] = useState(""),
    [info, setInfo] = useState<AppInfo | null>(null),
    [infoLoaded, setInfoLoaded] = useState(false);
  const [modal, setModal] = useState(false),
    [form, setForm] = useState<Config>(empty()),
    [separate, setSeparate] = useState(false),
    [launchMode, setLaunchMode] = useState<"vanilla" | "neoforge">("vanilla"),
    [formError, setFormError] = useState("");
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(""),
    [logs, setLogs] = useState<Log[]>([]),
    [command, setCommand] = useState("");
  const selectedRef = useRef(selected),
    logsRef = useRef<Log[]>([]),
    consoleRef = useRef<HTMLDivElement>(null),
    follow = useRef(true),
    pollCursor = useRef(0);
  selectedRef.current = selected;
  logsRef.current = logs;
  const current = views.find((v) => v.config.id === selected);
  const refresh = async () => {
    if (!api || !info?.capabilities.servers) return;
    const list = await api.ListServers();
    setViews(list || []);
    setSelected((s) =>
      list?.some((v) => v.config.id === s) ? s : list?.[0]?.config.id || "",
    );
  };
  useEffect(() => {
    if (!api) {
      setInfo({
        version: "",
        dataDir: "",
        error: "",
        edition: "client",
        capabilities: { servers: false, rooms: true, hosting: false },
      });
      setInfoLoaded(true);
      return;
    }
    api
      .GetAppInfo()
      .then((value) => {
        setInfo(value);
        setPage(value.capabilities.servers ? "servers" : "rooms");
        document.title =
          value.edition === "client" ? "GameDock Connect" : "GameDock";
      })
      .catch((e) => setError(errorText(e)))
      .finally(() => setInfoLoaded(true));
  }, []);
  useEffect(() => {
    if (!api || !info?.capabilities.servers) return;
    Promise.all([
      refresh(),
      api.GetSettings().then((value) => {
        setSettings(value);
        setSavedLibrary(value.libraryDir);
      }),
    ]).catch((e) => setError(errorText(e)));
  }, [info]);
  useEffect(() => {
    setLogs([]);
    pollCursor.current = 0;
    follow.current = true;
    if (!api || !info?.capabilities.servers || !selected) return;
    let alive = true;
    let inFlight = false;
    const poll = async () => {
      if (inFlight) return;
      inFlight = true;
      try {
        const runtime = await api.GetRuntime(selected);
        const last = pollCursor.current;
        const entries = await api.GetLogs(selected, last);
        if (!alive) return;
        pollCursor.current = (entries || []).reduce(
          (n, l) => Math.max(n, l.sequence),
          last,
        );
        setLogs((old) => {
          const merged = [...old, ...(entries || [])];
          const seen = new Set<string>();
          return merged
            .filter((l) => {
              const key = `${l.runId}:${l.sequence}`;
              if (seen.has(key)) return false;
              seen.add(key);
              return true;
            })
            .sort((a, b) => a.sequence - b.sequence)
            .slice(-1500);
        });
        setViews((old) =>
          old.map((v) => (v.config.id === selected ? { ...v, runtime } : v)),
        );
      } catch (e) {
        if (alive) setError(errorText(e));
      } finally {
        inFlight = false;
      }
    };
    poll();
    const timer = setInterval(poll, 1500);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [selected, info]);
  useEffect(() => {
    if (!api || !info?.capabilities.servers) return;
    const timer = setInterval(() => {
      refresh().catch((e) => setError(errorText(e)));
    }, 5000);
    return () => clearInterval(timer);
  }, [info]);
  useEffect(() => {
    if (!info?.capabilities.servers || !window.runtime?.EventsOn) return;
    return window.runtime.EventsOn("gamedock:event", (event: any) => {
      if (event?.snapshot)
        setViews((old) =>
          old.map((v) =>
            v.config.id === event.serverId
              ? { ...v, runtime: event.snapshot }
              : v,
          ),
        );
      if (event?.log && event.serverId === selectedRef.current)
        setLogs((old) =>
          old.some(
            (l) =>
              l.runId === event.log.runId && l.sequence === event.log.sequence,
          )
            ? old
            : [...old, event.log]
                .sort((a, b) => a.sequence - b.sequence)
                .slice(-1500),
        );
    });
  }, [info]);
  useEffect(() => {
    if (!window.runtime?.EventsOn) return;
    return window.runtime.EventsOn(
      "gamedock:shutdown-error",
      (message: unknown) => setError(errorText(message)),
    );
  }, []);
  useEffect(() => {
    const el = consoleRef.current;
    if (el && follow.current) el.scrollTop = el.scrollHeight;
  }, [logs]);
  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy((previous) => (previous === key ? "" : previous));
    }
  };
  const choose = async (
    kind: "dir" | "java" | "jar" | "args",
    field: keyof Config,
  ) => {
    if (!api) return;
    try {
      const value =
        kind === "dir"
          ? await api.PickDirectory(
              "Выберите папку",
              String(form[field]) || savedLibrary,
            )
          : kind === "java"
            ? await api.PickJava(form.javaPath)
            : kind === "jar"
              ? await api.PickJar(form.jarPath)
              : await api.PickArgsFile(form.javaArgsFile);
      if (value)
        setForm((f) => ({
          ...f,
          [field]: value,
          ...(field === "filesDir" && !separate && launchMode === "vanilla"
            ? { workingDir: value }
            : {}),
        }));
    } catch (e) {
      setFormError(errorText(e));
    }
  };
  const save = async () => {
    if (
      !form.name.trim() ||
      !form.javaPath.trim() ||
      (launchMode === "vanilla"
        ? !form.jarPath.trim()
        : !form.javaArgsFile.trim()) ||
      !form.filesDir.trim() ||
      (launchMode === "vanilla" && !form.workingDir.trim())
    ) {
      setFormError("Заполните название и все пути к файлам и папкам.");
      return;
    }
    if (
      !Number.isInteger(form.minMemoryMB) ||
      !Number.isInteger(form.maxMemoryMB) ||
      form.minMemoryMB < 256 ||
      form.maxMemoryMB > 1048576 ||
      form.maxMemoryMB < form.minMemoryMB
    ) {
      setFormError(
        "Проверьте объём памяти: от 256 до 1 048 576 МБ, максимум не меньше минимума.",
      );
      return;
    }
    if (!api) {
      setFormError("Сохранение доступно в настольном приложении.");
      return;
    }
    setBusy("save");
    setFormError("");
    try {
      const result = await api.SaveServer({
        ...form,
        name: form.name.trim(),
        javaArgsFile: launchMode === "vanilla" ? "" : form.javaArgsFile,
        workingDir: launchMode === "neoforge" ? form.filesDir : form.workingDir,
      });
      await refresh();
      setSelected(result.id);
      setModal(false);
    } catch (e) {
      setFormError(errorText(e));
    } finally {
      setBusy("");
    }
  };
  const openForm = (config?: Config) => {
    setForm(
      config
        ? { ...config, javaArgsFile: config.javaArgsFile || "" }
        : { ...empty(), filesDir: savedLibrary, workingDir: savedLibrary },
    );
    setSeparate(!!config && config.workingDir !== config.filesDir);
    setLaunchMode(config?.javaArgsFile ? "neoforge" : "vanilla");
    setFormError("");
    setModal(true);
  };
  const action = (type: "StartServer" | "StopServer" | "KillServer") =>
    current &&
    api &&
    run(type, async () => {
      await api[type](current.config.id);
      await refresh();
    });
  const remove = () => {
    if (
      !current ||
      !api ||
      !confirm(
        `Удалить «${current.config.name}» из GameDock? Файлы сервера останутся на диске.`,
      )
    )
      return;
    run("delete", async () => {
      await api.DeleteServer(current.config.id);
      await refresh();
    });
  };
  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">GD</div>
          <div>
            <b>
              {info?.edition === "client" ? "GameDock Connect" : "GameDock"}
            </b>
            <small>
              {info?.edition === "client" ? "ИГРАЙТЕ ВМЕСТЕ" : "SERVER MANAGER"}
            </small>
          </div>
        </div>
        <div className="side-label">Рабочее пространство</div>
        <nav>
          {info?.capabilities.servers && (
            <button
              className={page === "servers" ? "nav active" : "nav"}
              onClick={() => setPage("servers")}
            >
              <LayoutGrid size={18} /> Мои серверы{" "}
              <span>{views.length.toString().padStart(2, "0")}</span>
            </button>
          )}
          {info?.capabilities.rooms && (
            <button
              className={page === "rooms" ? "nav active" : "nav"}
              onClick={() => setPage("rooms")}
            >
              <Network size={18} /> Комнаты
            </button>
          )}
          {info?.capabilities.servers && (
            <button
              className={page === "storage" ? "nav active" : "nav"}
              onClick={() => setPage("storage")}
            >
              <HardDrive size={18} /> Хранилище
            </button>
          )}
        </nav>
        <div className="side-bottom">
          <div className="side-help">
            <div className="help-icon">
              <CircleHelp size={17} />
            </div>
            <div>
              <b>Локальные серверы</b>
              <p>Настройки хранятся на этом компьютере</p>
            </div>
          </div>
          <div className="version">
            <span className="status-dot" /> GameDock{" "}
            <span>v{info?.version || "0.2.0"}</span>
          </div>
        </div>
      </aside>
      <main>
        <header className="topbar">
          <div className="breadcrumbs">
            Рабочее пространство <span>/</span>{" "}
            <b>
              {page === "servers"
                ? "Мои серверы"
                : page === "rooms"
                  ? "Комнаты"
                  : "Хранилище"}
            </b>
          </div>
          <div className="top-right">
            <span className="local-dot" /> Локальный режим{" "}
            <div className="avatar">G</div>
          </div>
        </header>
        {!api && (
          <div className="notice">
            Предпросмотр интерфейса: подключение к настольному приложению
            недоступно. Управление серверами отключено.
          </div>
        )}
        {error && (
          <div className="notice error">
            <span>{error}</span>
            <button onClick={() => setError("")}>
              <X size={16} />
            </button>
          </div>
        )}
        {info?.error && <div className="notice error">{info.error}</div>}
        {!infoLoaded ? (
          <div className="content">Загрузка приложения…</div>
        ) : !info ? null : page === "servers" && info.capabilities.servers ? (
          <div className="content">
            <div className="page-heading">
              <div>
                <div className="eyebrow">
                  ВАШЕ ПРОСТРАНСТВО <span> / </span> ОБЗОР
                </div>
                <h1>
                  Мои серверы <span className="count">{views.length}</span>
                </h1>
                <p>Управляйте игровыми мирами в одном месте.</p>
              </div>
              <button className="primary" onClick={() => openForm()}>
                <Plus size={18} /> Добавить сервер
              </button>
            </div>
            <div className="workspace">
              <section className="server-column">
                <div className="section-head">
                  <div>
                    <span className="section-title">Библиотека</span>
                    <span className="section-sub">{views.length} серверов</span>
                  </div>
                  <button
                    className="icon-button"
                    title="Обновить"
                    onClick={() => run("refresh", refresh)}
                  >
                    <RefreshCw size={16} />
                  </button>
                </div>
                <div className="filter">
                  <span>ВСЕ СЕРВЕРЫ</span>
                  <ChevronDown size={14} />
                </div>
                <div className="server-list">
                  {views.length === 0 ? (
                    <div className="list-empty">
                      <div className="empty-icon">
                        <Server size={22} />
                      </div>
                      <b>Здесь пока тихо</b>
                      <p>Добавьте первый сервер, чтобы начать.</p>
                      <button onClick={() => openForm()}>
                        <Plus size={15} /> Добавить сервер
                      </button>
                    </div>
                  ) : (
                    views.map((v) => (
                      <button
                        key={v.config.id}
                        className={
                          "server-item " +
                          (selected === v.config.id ? "selected" : "")
                        }
                        onClick={() => setSelected(v.config.id)}
                      >
                        <div className="game-icon">
                          <Box size={21} />
                        </div>
                        <div className="server-text">
                          <strong>{v.config.name}</strong>
                          <small>
                            MINECRAFT <span>·</span>{" "}
                            {v.available ? "ЛОКАЛЬНЫЙ" : "НЕДОСТУПЕН"}
                          </small>
                        </div>
                        <span
                          className={
                            "item-dot " +
                            (v.runtime.state === "running"
                              ? "on"
                              : v.runtime.state === "failed"
                                ? "bad"
                                : "")
                          }
                        />
                      </button>
                    ))
                  )}
                </div>
                <div className="list-footer">
                  <span>СЕРВЕРОВ ВСЕГО</span>
                  <b>{views.length.toString().padStart(2, "0")}</b>
                </div>
              </section>
              <section className="detail">
                {current ? (
                  <>
                    <div className="detail-header">
                      <div className="detail-title">
                        <div className="game-icon large">
                          <Box size={27} />
                        </div>
                        <div>
                          <div className="detail-kicker">MINECRAFT SERVER</div>
                          <h2>{current.config.name}</h2>
                          <div className="status-line">
                            <span
                              className={
                                "status-dot " +
                                (current.runtime.state === "running"
                                  ? "on"
                                  : current.runtime.state === "failed"
                                    ? "bad"
                                    : "")
                              }
                            />
                            {states[current.runtime.state] ||
                              current.runtime.state}{" "}
                            <span className="sep">·</span>{" "}
                            {current.available
                              ? "Файлы доступны"
                              : "Файлы недоступны"}
                          </div>
                        </div>
                      </div>
                      <div className="actions">
                        <button
                          className="secondary"
                          disabled={active(current.runtime) || !!busy}
                          onClick={() => openForm(current.config)}
                        >
                          <SettingsIcon size={16} /> Настроить
                        </button>
                        {current.runtime.state === "running" ||
                        current.runtime.state === "starting" ? (
                          <button
                            className="stop"
                            disabled={!api || !!busy}
                            onClick={() => action("StopServer")}
                          >
                            <Square size={14} fill="currentColor" /> Остановить
                          </button>
                        ) : (
                          <button
                            className="primary"
                            disabled={
                              !api ||
                              !!busy ||
                              active(current.runtime) ||
                              !current.available
                            }
                            onClick={() => action("StartServer")}
                          >
                            <Play size={15} fill="currentColor" /> Запустить
                          </button>
                        )}
                      </div>
                    </div>
                    {!current.available && (
                      <div className="availability">
                        {current.availabilityError ||
                          "Путь к файлам недоступен. Проверьте подключение диска."}
                      </div>
                    )}
                    {current.runtime.error && (
                      <div className="availability">
                        {current.runtime.error}
                      </div>
                    )}
                    <div className="metrics">
                      <div>
                        <small>Процесс</small>
                        <strong>
                          <span
                            className={
                              "status-dot " +
                              (current.runtime.state === "running" ? "on" : "")
                            }
                          />
                          {states[current.runtime.state] ||
                            current.runtime.state}
                        </strong>
                      </div>
                      <div>
                        <small>Память</small>
                        <strong>
                          {current.config.minMemoryMB}–
                          {current.config.maxMemoryMB} <em>МБ</em>
                        </strong>
                      </div>
                      <div>
                        <small>ПРОЦЕСС</small>
                        <strong>
                          {current.runtime.pid
                            ? `PID ${current.runtime.pid}`
                            : "—"}
                        </strong>
                      </div>
                      <div>
                        <small>Рабочая папка</small>
                        <strong
                          className="path"
                          title={current.config.workingDir}
                        >
                          {current.config.workingDir || "—"}
                        </strong>
                      </div>
                    </div>
                    <div className="console-wrap">
                      <div className="console-head">
                        <div>
                          <Terminal size={17} />
                          <b>Консоль</b>
                          <span>Журнал процесса</span>
                        </div>
                        <div className="console-tools">
                          {active(current.runtime) && (
                            <button
                              disabled={
                                !api || (!!busy && busy !== "StopServer")
                              }
                              onClick={() => {
                                if (
                                  confirm(
                                    "Принудительное завершение может привести к потере несохранённых данных мира. Продолжить?",
                                  )
                                )
                                  action("KillServer");
                              }}
                            >
                              Завершить принудительно
                            </button>
                          )}
                          <span className="live">
                            <i /> LIVE
                          </span>
                        </div>
                      </div>
                      <div
                        className="console"
                        ref={consoleRef}
                        onScroll={(e) => {
                          const el = e.currentTarget;
                          follow.current =
                            el.scrollHeight - el.scrollTop - el.clientHeight <
                            40;
                        }}
                      >
                        {logs.length ? (
                          logs.map((l, i) => (
                            <div
                              className="log-line"
                              key={`${l.runId}-${l.sequence}-${i}`}
                            >
                              <span>
                                {new Date(l.time).toLocaleTimeString("ru-RU")}
                              </span>
                              <em
                                className={
                                  l.stream === "stderr" ? "stderr" : ""
                                }
                              >
                                {l.stream === "stderr" ? "ERROR" : "INFO"}
                              </em>
                              <code>{l.message}</code>
                            </div>
                          ))
                        ) : (
                          <div className="console-empty">
                            <Terminal size={22} />
                            <b>Консоль сервера</b>
                            <p>Здесь появится вывод процесса после запуска.</p>
                          </div>
                        )}
                      </div>
                      <form
                        className="command"
                        onSubmit={(e) => {
                          e.preventDefault();
                          if (!api || !command.trim()) return;
                          const value = command;
                          run("command", async () => {
                            await api.SendCommand(current.config.id, value);
                            setCommand("");
                          });
                        }}
                      >
                        <span>›</span>
                        <input
                          value={command}
                          onChange={(e) => setCommand(e.target.value)}
                          placeholder="Введите команду сервера..."
                          disabled={!api || current.runtime.state !== "running"}
                        />
                        <button
                          disabled={
                            !api ||
                            current.runtime.state !== "running" ||
                            !command.trim() ||
                            !!busy
                          }
                        >
                          Отправить <span>↵</span>
                        </button>
                      </form>
                    </div>
                    <div className="detail-foot">
                      <span>
                        <Activity size={15} /> Статус показывает состояние
                        процесса. Готовность сервера проверяйте в консоли.
                      </span>
                      <button
                        disabled={active(current.runtime) || !!busy}
                        onClick={remove}
                      >
                        <Trash2 size={14} /> Удалить
                      </button>
                    </div>
                  </>
                ) : (
                  <div className="detail-empty">
                    <div className="empty-orbit">
                      <Server size={32} />
                    </div>
                    <div className="eyebrow">ВАША ИГРА. ВАШ СЕРВЕР.</div>
                    <h2>Выберите сервер</h2>
                    <p>
                      Добавьте свой первый Minecraft-сервер,
                      <br /> чтобы управлять им из GameDock.
                    </p>
                    <button className="primary" onClick={() => openForm()}>
                      <Plus size={17} /> Добавить сервер
                    </button>
                    <div className="empty-notes">
                      <span>
                        <Check size={15} /> Локальное управление
                      </span>
                      <span>
                        <Check size={15} /> Консоль в реальном времени
                      </span>
                    </div>
                  </div>
                )}
              </section>
            </div>
          </div>
        ) : page === "rooms" ? (
          info.capabilities.rooms ? (
            <Rooms
              api={api as unknown as RoomAPI | undefined}
              servers={views}
              canHost={info.capabilities.hosting}
            />
          ) : null
        ) : (
          <div className="content storage-page">
            <div className="page-heading">
              <div>
                <div className="eyebrow">
                  Рабочее пространство <span> / </span> НАСТРОЙКИ
                </div>
                <h1>Хранилище</h1>
                <p>Выберите удобное место для ваших игровых файлов.</p>
              </div>
            </div>
            <div className="storage-card">
              <div className="storage-title">
                <div className="storage-icon">
                  <HardDrive size={21} />
                </div>
                <div>
                  <h2>Папка библиотеки</h2>
                  <p>Ориентир для размещения файлов серверов</p>
                </div>
              </div>
              <label>ПУТЬ К БИБЛИОТЕКЕ</label>
              <div className="input-row">
                <input
                  value={settings.libraryDir}
                  onChange={(e) => setSettings({ libraryDir: e.target.value })}
                  placeholder="Выберите папку на диске"
                />
                <button
                  className="secondary"
                  disabled={!api}
                  onClick={() =>
                    run("pick", async () => {
                      const p = await api!.PickDirectory(
                        "Выберите папку библиотеки",
                        settings.libraryDir,
                      );
                      if (p) setSettings({ libraryDir: p });
                    })
                  }
                >
                  <FolderOpen size={16} /> Обзор
                </button>
              </div>
              <p className="hint">
                Можно выбрать папку на внешнем SSD UGREEN или любом другом
                диске. GameDock не создаёт и не переносит файлы автоматически.
              </p>
              <button
                className="primary"
                disabled={!api || !!busy}
                onClick={() =>
                  run("settings", async () => {
                    const saved = await api!.SaveSettings(settings);
                    setSettings(saved);
                    setSavedLibrary(saved.libraryDir);
                  })
                }
              >
                <Save size={16} /> Сохранить
              </button>
            </div>
            <div className="storage-info">
              <Folder size={18} />
              <div>
                <b>Данные приложения</b>
                <p>
                  Настройки и записи серверов хранятся локально
                  {info?.dataDir ? `: ${info.dataDir}` : "."}
                </p>
              </div>
            </div>
          </div>
        )}
      </main>
      {modal && (
        <div
          className="overlay"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setModal(false);
          }}
        >
          <div className="modal">
            <div className="modal-head">
              <div>
                <div className="eyebrow">MINECRAFT SERVER</div>
                <h2>{form.id ? "Настройки сервера" : "Новый сервер"}</h2>
                <p>Укажите существующие файлы сервера и параметры запуска.</p>
              </div>
              <button className="icon-button" onClick={() => setModal(false)}>
                <X size={19} />
              </button>
            </div>
            <div className="modal-body">
              {formError && <div className="form-error">{formError}</div>}
              <label>
                НАЗВАНИЕ СЕРВЕРА
                <input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="Например, Мир друзей"
                />
              </label>
              <label>
                ТИП ЗАПУСКА
                <select
                  value={launchMode}
                  onChange={(e) => {
                    const mode = e.target.value as "vanilla" | "neoforge";
                    setLaunchMode(mode);
                    if (mode === "vanilla" && !separate)
                      setForm((f) => ({ ...f, workingDir: f.filesDir }));
                  }}
                >
                  <option value="vanilla">Vanilla JAR</option>
                  <option value="neoforge">NeoForge — файл аргументов</option>
                </select>
              </label>
              <div className="form-grid">
                <label>
                  JAVA
                  <input
                    value={form.javaPath}
                    onChange={(e) =>
                      setForm({ ...form, javaPath: e.target.value })
                    }
                    placeholder="Путь к java"
                  />
                  <button
                    className="browse"
                    onClick={() => choose("java", "javaPath")}
                    disabled={!api}
                  >
                    <FolderOpen size={16} />
                  </button>
                </label>
                {launchMode === "vanilla" ? (
                  <label>
                    JAR-ФАЙЛ СЕРВЕРА
                    <input
                      value={form.jarPath}
                      onChange={(e) =>
                        setForm({ ...form, jarPath: e.target.value })
                      }
                      placeholder="Путь к server.jar"
                    />
                    <button
                      className="browse"
                      onClick={() => choose("jar", "jarPath")}
                      disabled={!api}
                    >
                      <FolderOpen size={16} />
                    </button>
                  </label>
                ) : (
                  <label>
                    ФАЙЛ АРГУМЕНТОВ NEOFORGE (WIN_ARGS.TXT)
                    <input
                      value={form.javaArgsFile}
                      onChange={(e) =>
                        setForm({ ...form, javaArgsFile: e.target.value })
                      }
                      placeholder="Путь к win_args.txt"
                    />
                    <button
                      className="browse"
                      onClick={() => choose("args", "javaArgsFile")}
                      disabled={!api}
                    >
                      <FolderOpen size={16} />
                    </button>
                  </label>
                )}
              </div>
              <label>
                ПАПКА ФАЙЛОВ СЕРВЕРА
                <input
                  value={form.filesDir}
                  onChange={(e) =>
                    setForm({
                      ...form,
                      filesDir: e.target.value,
                      ...(!separate && launchMode === "vanilla"
                        ? { workingDir: e.target.value }
                        : {}),
                    })
                  }
                  placeholder="Путь к существующей папке"
                />
                <button
                  className="browse"
                  onClick={() => choose("dir", "filesDir")}
                  disabled={!api}
                >
                  <FolderOpen size={16} />
                </button>
              </label>
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={launchMode === "vanilla" && separate}
                  disabled={launchMode === "neoforge"}
                  onChange={(e) => {
                    setSeparate(e.target.checked);
                    if (!e.target.checked)
                      setForm({ ...form, workingDir: form.filesDir });
                  }}
                />{" "}
                Использовать отдельную рабочую папку
              </label>
              {launchMode === "neoforge" && (
                <p className="hint">
                  Для NeoForge рабочая папка совпадает с папкой файлов сервера:
                  загрузчик использует относительные пути к библиотекам.
                </p>
              )}
              {launchMode === "vanilla" && separate && (
                <label>
                  Рабочая папка
                  <input
                    value={form.workingDir}
                    onChange={(e) =>
                      setForm({ ...form, workingDir: e.target.value })
                    }
                    placeholder="Путь к рабочей папке"
                  />
                  <button
                    className="browse"
                    onClick={() => choose("dir", "workingDir")}
                    disabled={!api}
                  >
                    <FolderOpen size={16} />
                  </button>
                </label>
              )}
              <div className="form-grid">
                <label>
                  МИНИМУМ ПАМЯТИ, МБ
                  <input
                    type="number"
                    min="256"
                    max="1048576"
                    value={form.minMemoryMB}
                    onChange={(e) =>
                      setForm({ ...form, minMemoryMB: Number(e.target.value) })
                    }
                  />
                </label>
                <label>
                  МАКСИМУМ ПАМЯТИ, МБ
                  <input
                    type="number"
                    min="256"
                    max="1048576"
                    value={form.maxMemoryMB}
                    onChange={(e) =>
                      setForm({ ...form, maxMemoryMB: Number(e.target.value) })
                    }
                  />
                </label>
              </div>
              <p className="hint">
                Используйте существующие Java и файлы сервера. В рабочей папке
                вручную примите EULA Minecraft: создайте или измените eula.txt,
                указав eula=true. GameDock не создаёт папки автоматически.
              </p>
            </div>
            <div className="modal-foot">
              <button className="secondary" onClick={() => setModal(false)}>
                Отмена
              </button>
              <button className="primary" disabled={!!busy} onClick={save}>
                <Check size={16} />{" "}
                {busy === "save" ? "Сохранение..." : "Сохранить сервер"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
createRoot(document.getElementById("root")!).render(<App />);
