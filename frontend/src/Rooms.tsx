import { useEffect, useState } from "react";
import {
  Copy,
  Download,
  Link,
  Plus,
  Radio,
  Trash2,
  Users,
  X,
} from "lucide-react";
import "./rooms.css";

type Peer = {
  id: string;
  hostname: string;
  virtualIp: string;
  connection: string;
  latencyMs: number;
};
type RoomStatus = {
  roomId: string;
  state: string;
  address: string;
  virtualIp: string;
  error: string;
  allowRelay: boolean;
  peers: Peer[];
  logs: string[];
};
type Room = {
  config: {
    id: string;
    name: string;
    role: string;
    serverId: string;
    gamePort: number;
    localPort: number;
    hostVirtualIp: string;
    allowRelay: boolean;
    bootstrapPeers: string[];
    createdAt: string;
  };
  status?: RoomStatus;
};
type Engine = {
  installed: boolean;
  version: string;
  path: string;
  error: string;
  installing?: boolean;
};
export type RoomAPI = {
  ListRooms: () => Promise<Room[]>;
  CreateRoom: (request: {
    name: string;
    serverId: string;
    gamePort: number;
    allowRelay: boolean;
    bootstrapPeers?: string[];
  }) => Promise<Room>;
  JoinRoom: (
    invitation: string,
    localPort: number,
    allowRelay: boolean,
  ) => Promise<Room>;
  DeleteRoom: (id: string) => Promise<void>;
  ConnectRoom: (id: string) => Promise<void>;
  DisconnectRoom: () => Promise<void>;
  GetRoomStatus: () => Promise<RoomStatus>;
  GetRoomInvitation: (id: string) => Promise<string>;
  GetNetworkEngine: () => Promise<Engine>;
  InstallNetworkEngine: () => Promise<Engine>;
  SaveRoomSettings: (
    id: string,
    localPort: number,
    allowRelay: boolean,
    bootstrapPeers: string[],
  ) => Promise<void>;
  CopyText?: (text: string) => Promise<void>;
};
type ServerView = {
  config: { id: string; name: string };
  runtime: { state: string };
};
const stateText: Record<string, string> = {
  starting: "Запуск сети",
  connecting: "Подключение",
  waiting: "Ожидание участников",
  connected: "Подключено",
  no_direct_path: "Нет прямого соединения",
  error: "Ошибка",
  disconnected: "Отключено",
};
const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const defaultBootstrap =
  "tcp://public.easytier.cn:11010\ntcp://public.easytier.top:11010";

export default function Rooms({
  api,
  servers = [],
  canHost = false,
}: {
  api?: RoomAPI;
  servers?: ServerView[];
  canHost?: boolean;
}) {
  const [rooms, setRooms] = useState<Room[]>([]);
  const [status, setStatus] = useState<RoomStatus>();
  const [engine, setEngine] = useState<Engine>();
  const [dialog, setDialog] = useState<"create" | "join" | "edit" | "">("");
  const [editing, setEditing] = useState("");
  const [editingHost, setEditingHost] = useState(false);
  const [name, setName] = useState("");
  const [serverId, setServerId] = useState("");
  const [gamePort, setGamePort] = useState(25565);
  const [localPort, setLocalPort] = useState(25566);
  const [allowRelay, setAllowRelay] = useState(false);
  const [bootstrap, setBootstrap] = useState(defaultBootstrap);
  const [invitation, setInvitation] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const available = !!api?.ListRooms;
  const refresh = async () => {
    if (!api || !available) return;
    const [list, current, info] = await Promise.all([
      api.ListRooms(),
      api.GetRoomStatus(),
      api.GetNetworkEngine(),
    ]);
    setRooms(list || []);
    setStatus(current);
    setEngine(info);
  };
  useEffect(() => {
    if (!available) return;
    let live = true,
      polling = false;
    const poll = async () => {
      if (polling) return;
      polling = true;
      try {
        const [list, current, info] = await Promise.all([
          api!.ListRooms(),
          api!.GetRoomStatus(),
          api!.GetNetworkEngine(),
        ]);
        if (live) {
          setRooms(list || []);
          setStatus(current);
          setEngine(info);
        }
      } catch (e) {
        if (live) setError(message(e));
      } finally {
        polling = false;
      }
    };
    poll();
    const timer = setInterval(poll, 1500);
    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [available]);
  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    setError("");
    setNotice("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy("");
    }
  };
  const copy = async (text: string) => {
    if (navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(text);
        return;
      } catch {
        // The desktop webview may reject clipboard access; use its native bridge.
      }
    }
    if (api?.CopyText) await api.CopyText(text);
    else throw new Error("Не удалось получить доступ к буферу обмена.");
  };
  const copyInvitation = (id: string) =>
    run("copy", async () => {
      const value = await api!.GetRoomInvitation(id);
      await copy(value);
      setNotice(
        "Приглашение скопировано. Отправляйте его только тем, кому доверяете.",
      );
    });
  const copyAddress = (address: string) =>
    run("copy", async () => {
      await copy(address);
      setNotice("Адрес скопирован.");
    });
  const submit = () => {
    if (dialog === "create" && (!name.trim() || !serverId)) {
      setError("Укажите название комнаты и выберите сервер.");
      return;
    }
    if (dialog === "join" && !invitation.trim()) {
      setError("Вставьте приглашение в комнату.");
      return;
    }
    if (dialog === "create" && !canHost) return;
    const port = dialog === "create" ? gamePort : localPort;
    if (
      !(dialog === "edit" && editingHost) &&
      (!Number.isInteger(port) || port < 1 || port > 65535)
    ) {
      setError("Порт должен быть целым числом от 1 до 65535.");
      return;
    }
    run("submit", async () => {
      if (dialog === "create")
        await api!.CreateRoom({
          name: name.trim(),
          serverId,
          gamePort,
          allowRelay,
          bootstrapPeers: bootstrap
            .split(/\r?\n/)
            .map((s) => s.trim())
            .filter(Boolean),
        });
      else if (dialog === "edit")
        await api!.SaveRoomSettings(
          editing,
          localPort,
          allowRelay,
          bootstrap
            .split(/\r?\n/)
            .map((s) => s.trim())
            .filter(Boolean),
        );
      else await api!.JoinRoom(invitation.trim(), localPort, allowRelay);
      setInvitation("");
      setDialog("");
    });
  };
  const activeRoom =
    status?.roomId && !["disconnected", "error"].includes(status.state);
  return (
    <div className="content rooms-page">
      <div className="page-heading">
        <div>
          <div className="eyebrow">
            ВАШЕ ПРОСТРАНСТВО <span> / </span> СЕТЬ
          </div>
          <h1>
            Комнаты <span className="count">{rooms.length}</span>
          </h1>
          <p>Играйте вместе через защищённую виртуальную сеть.</p>
        </div>
        <div className="rooms-actions">
          <button
            className="secondary"
            onClick={() => {
              setDialog("join");
              setError("");
              setAllowRelay(false);
            }}
          >
            <Link size={16} /> Присоединиться
          </button>
          {canHost && (
            <button
              className="primary"
              onClick={() => {
                setDialog("create");
                setError("");
                setAllowRelay(false);
                setBootstrap(defaultBootstrap);
                if (!serverId && servers.length)
                  setServerId(servers[0].config.id);
              }}
            >
              <Plus size={17} /> Создать комнату
            </button>
          )}
        </div>
      </div>
      {!available && (
        <div className="notice room-banner">
          Управление комнатами доступно в настольном приложении с поддержкой
          сети.
        </div>
      )}
      {error && (
        <div className="notice error room-banner">
          <span>{error}</span>
          <button onClick={() => setError("")}>
            <X size={16} />
          </button>
        </div>
      )}
      {notice && <div className="notice room-banner">{notice}</div>}
      <div className="engine-card">
        <div className="storage-icon">
          <Radio size={21} />
        </div>
        <div className="engine-copy">
          <b>Сетевой движок EasyTier</b>
          <p>
            {!available
              ? "Недоступен в предпросмотре"
              : !engine
                ? "Проверка состояния…"
                : engine.installed
                  ? `Установлен${engine.version ? ` · ${engine.version}` : ""}`
                  : engine?.installing || busy === "install"
                    ? "Установка…"
                    : "Не установлен"}
            {engine?.error ? ` · ${engine.error}` : ""}
          </p>
        </div>
        {available && engine && !engine.installed && (
          <button
            className="secondary"
            disabled={!!busy || !!engine?.installing}
            onClick={() =>
              run("install", async () => {
                setEngine(await api!.InstallNetworkEngine());
              })
            }
          >
            <Download size={16} /> Установить
          </button>
        )}
      </div>
      {rooms.length === 0 ? (
        <div className="rooms-empty">
          <div className="empty-orbit">
            <Users size={29} />
          </div>
          <h2>Комнаты для игры</h2>
          <p>
            {canHost
              ? "Создайте комнату для своего сервера или войдите по приглашению друга."
              : "Войдите по приглашению друга, чтобы играть вместе."}
          </p>
          <div className="rooms-actions">
            <button
              className="secondary"
              onClick={() => {
                setDialog("join");
                setError("");
                setAllowRelay(false);
              }}
            >
              Войти по приглашению
            </button>
            {canHost && (
              <button
                className="primary"
                onClick={() => {
                  setDialog("create");
                  setError("");
                  setAllowRelay(false);
                  setBootstrap(defaultBootstrap);
                  if (!serverId && servers.length)
                    setServerId(servers[0].config.id);
                }}
              >
                Создать комнату
              </button>
            )}
          </div>
        </div>
      ) : (
        <div className="room-grid">
          {rooms.map((room) => {
            const current =
              status?.roomId === room.config.id ? status : undefined;
            const state = current?.state || "disconnected";
            const isActive = !!activeRoom && status?.roomId === room.config.id;
            return (
              <article className="room-card" key={room.config.id}>
                <div className="room-card-head">
                  <div className="game-icon">
                    <Users size={19} />
                  </div>
                  <div>
                    <h2>{room.config.name}</h2>
                    <p>{room.config.role === "host" ? "Владелец" : "Гость"}</p>
                  </div>
                  <span
                    className={
                      "room-state " +
                      (state === "connected"
                        ? "connected"
                        : state === "error" || state === "no_direct_path"
                          ? "problem"
                          : "")
                    }
                  >
                    <i />
                    {stateText[state] || state}
                  </span>
                </div>
                {current?.error && (
                  <div className="room-error">{current.error}</div>
                )}
                <div className="room-address">
                  <div>
                    <small>
                      {room.config.role === "host"
                        ? "АДРЕС СЕРВЕРА НА ЭТОМ КОМПЬЮТЕРЕ"
                        : "ЛОКАЛЬНЫЙ АДРЕС ДЛЯ MINECRAFT"}
                    </small>
                    <strong>
                      {current?.address || "Появится после подключения"}
                    </strong>
                  </div>
                  {current?.address && (
                    <button
                      className="icon-button"
                      title="Копировать адрес"
                      onClick={() => copyAddress(current.address)}
                    >
                      <Copy size={16} />
                    </button>
                  )}
                </div>
                {current?.peers?.length ? (
                  <div className="room-peers">
                    <small>Участники</small>
                    {current.peers.map((peer) => (
                      <div className="peer" key={peer.id}>
                        <span>
                          {peer.hostname || peer.virtualIp || peer.id}
                        </span>
                        <span>
                          {peer.connection === "direct"
                            ? "Напрямую"
                            : peer.connection === "relay"
                              ? "Через посредника"
                              : peer.connection || "—"}
                          {peer.latencyMs >= 0 ? ` · ${peer.latencyMs} мс` : ""}
                        </span>
                      </div>
                    ))}
                  </div>
                ) : (
                  <p className="room-no-peers">
                    Участники появятся после подключения.
                  </p>
                )}
                {!!current?.logs?.length && (
                  <details className="room-logs">
                    <summary>Журнал сети</summary>
                    <pre>{current.logs.slice(-100).join("\n")}</pre>
                  </details>
                )}
                <div className="room-footer">
                  {isActive ? (
                    <button
                      className="secondary"
                      disabled={!available || !!busy}
                      onClick={() =>
                        run("disconnect", async () => {
                          await api!.DisconnectRoom();
                        })
                      }
                    >
                      Отключить
                    </button>
                  ) : (
                    <button
                      className="primary"
                      disabled={
                        !available ||
                        !!busy ||
                        !engine?.installed ||
                        !!activeRoom
                      }
                      onClick={() =>
                        run("connect", async () => {
                          await api!.ConnectRoom(room.config.id);
                        })
                      }
                    >
                      Подключить
                    </button>
                  )}
                  {canHost && room.config.role === "host" && (
                    <button
                      className="secondary"
                      disabled={!available || !!busy}
                      onClick={() => copyInvitation(room.config.id)}
                    >
                      <Copy size={14} /> Приглашение
                    </button>
                  )}
                  {(room.config.role !== "host" || canHost) && (
                    <button
                      className="secondary"
                      disabled={!available || !!busy || isActive}
                      title={
                        isActive
                          ? "Отключите комнату перед изменением настроек"
                          : "Настройки подключения"
                      }
                      onClick={() => {
                        setEditing(room.config.id);
                        setEditingHost(room.config.role === "host");
                        setLocalPort(room.config.localPort);
                        setAllowRelay(room.config.allowRelay);
                        setBootstrap(
                          (room.config.bootstrapPeers || []).join("\n"),
                        );
                        setError("");
                        setDialog("edit");
                      }}
                    >
                      Настроить
                    </button>
                  )}
                  <button
                    className="icon-button room-delete"
                    disabled={!available || !!busy || isActive}
                    title="Удалить комнату"
                    onClick={() => {
                      if (
                        confirm(
                          `Удалить комнату «${room.config.name}» из GameDock?`,
                        )
                      )
                        run("delete", async () => {
                          await api!.DeleteRoom(room.config.id);
                        });
                    }}
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      )}
      {dialog && (
        <div
          className="overlay"
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setDialog("");
          }}
        >
          <div className="modal">
            <div className="modal-head">
              <div>
                <div className="eyebrow">СЕТЬ GAMEDOCK</div>
                <h2>
                  {dialog === "create"
                    ? "Создать комнату"
                    : dialog === "edit"
                      ? "Настройки подключения"
                      : "Войти в комнату"}
                </h2>
                <p>
                  {dialog === "create"
                    ? "Выберите сервер и поделитесь приглашением с друзьями."
                    : dialog === "edit"
                      ? "Измените параметры отключённой комнаты."
                      : "Вставьте личное приглашение владельца комнаты."}
                </p>
              </div>
              <button className="icon-button" onClick={() => setDialog("")}>
                <X size={19} />
              </button>
            </div>
            <div className="modal-body">
              {error && <div className="form-error">{error}</div>}
              {dialog === "create" ? (
                <>
                  <label>
                    Название комнаты
                    <input
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="Например, Мир друзей"
                    />
                  </label>
                  <label>
                    СЕРВЕР
                    <select
                      value={serverId}
                      onChange={(e) => setServerId(e.target.value)}
                    >
                      <option value="">Выберите сервер</option>
                      {servers.map((s) => (
                        <option key={s.config.id} value={s.config.id}>
                          {s.config.name} ·{" "}
                          {s.runtime.state === "running"
                            ? "процесс запущен"
                            : "не запущен"}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label>
                    ПОРТ MINECRAFT
                    <input
                      type="number"
                      min="1"
                      max="65535"
                      value={gamePort}
                      onChange={(e) => setGamePort(Number(e.target.value))}
                    />
                  </label>
                  <p className="hint">
                    Перед подключением комнаты запустите выбранный сервер
                    Minecraft. Укажите порт из его настроек.
                  </p>
                </>
              ) : (
                <>
                  {dialog === "join" && (
                    <>
                      <label>
                        Приглашение
                        <input
                          type="password"
                          autoComplete="off"
                          value={invitation}
                          onChange={(e) => setInvitation(e.target.value)}
                          placeholder="Вставьте приглашение"
                        />
                      </label>
                      <p className="hint">
                        Приглашение содержит секретный ключ. Не публикуйте его и
                        не передавайте незнакомым людям.
                      </p>
                    </>
                  )}
                  {!(dialog === "edit" && editingHost) && (
                    <>
                      <label>
                        Локальный порт
                        <input
                          type="number"
                          min="1"
                          max="65535"
                          value={localPort}
                          onChange={(e) => setLocalPort(Number(e.target.value))}
                        />
                      </label>
                      <p className="hint">
                        Гость подключается из Minecraft к локальному адресу,
                        который появится после соединения. Сервер и Java для
                        GameDock гостю не нужны.
                      </p>
                    </>
                  )}
                </>
              )}
              <label className="checkbox">
                <input
                  type="checkbox"
                  checked={allowRelay}
                  onChange={(e) => setAllowRelay(e.target.checked)}
                />{" "}
                Разрешить соединение через посредника
              </label>
              <p className="hint">
                Если прямое соединение невозможно, разрешить передачу игрового
                трафика через узел-посредник.
              </p>
              {(dialog === "create" || dialog === "edit") && (
                <details className="bootstrap-settings">
                  <summary>Дополнительные настройки сети</summary>
                  <label>
                    НАЧАЛЬНЫЕ УЗЛЫ (ПО ОДНОМУ АДРЕСУ В СТРОКЕ)
                    <textarea
                      value={bootstrap}
                      onChange={(e) => setBootstrap(e.target.value)}
                      rows={3}
                    />
                  </label>
                  <p className="hint">
                    Общедоступные узлы помогают участникам находить друг друга.
                    Они не предоставляют полный доступ в интернет.
                  </p>
                </details>
              )}
            </div>
            <div className="modal-foot">
              <button className="secondary" onClick={() => setDialog("")}>
                Отмена
              </button>
              <button
                className="primary"
                disabled={!available || !!busy}
                onClick={submit}
              >
                {dialog === "create"
                  ? "Создать комнату"
                  : dialog === "edit"
                    ? "Сохранить"
                    : "Добавить комнату"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
