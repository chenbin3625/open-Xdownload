import { useEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import {
  Link,
  NavLink,
  Route,
  Routes,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Banner,
  Button,
  Input,
  InputNumber,
  Modal,
  Pagination,
  Progress,
  Select,
  Spin,
  Switch,
  Tag,
  Toast,
  Tooltip,
} from "@douyinfe/semi-ui";
import {
  Archive,
  ArrowDownToLine,
  ArrowLeft,
  ArrowUpRight,
  Check,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  Clock3,
  Download,
  ExternalLink,
  FolderOpen,
  Heart,
  Image,
  Info,
  LayoutDashboard,
  ListFilter,
  LockKeyhole,
  LogOut,
  Maximize2,
  Play,
  Plus,
  RefreshCw,
  Search,
  Settings as SettingsIcon,
  Tags,
  Trash2,
  Users,
  Video,
  X,
} from "lucide-react";
import PhotoSwipe from "photoswipe";
import { version } from "../package.json";
import { bytes, date, params, request, send } from "./api";
import type {
  CookieStatus,
  Dashboard,
  Media,
  Member,
  Page,
  Settings,
  SettingsResponse,
  Snapshot,
  Task,
  TaskGroup,
  Tweet,
  User,
} from "./api";
import { useUI } from "./store";

const statusNames: Record<string, string> = {
  queued: "等待中",
  running: "进行中",
  cancelling: "取消中",
  succeeded: "已完成",
  failed: "失败",
  cancelled: "已取消",
  pending: "待下载",
  downloading: "下载中",
  ready: "已下载",
  complete: "完整",
  partial: "部分",
  missing: "未设置",
  unknown: "待检测",
  valid: "有效",
  invalid: "已失效",
  limited: "访问受限",
  enabled: "已启用",
  paused: "已暂停",
};
function Status({ value }: { value: string }) {
  const color = ["valid", "ready", "succeeded", "complete", "enabled"].includes(value)
    ? "green"
    : ["invalid", "failed"].includes(value)
      ? "red"
      : ["partial", "limited", "unknown"].includes(value)
        ? "orange"
        : "grey";
  return (
    <Tag color={color} size="small" shape="square">
      {statusNames[value] || value}
    </Tag>
  );
}
function IconButton({
  label,
  icon,
  onClick,
  disabled = false,
  active = false,
}: {
  label: string;
  icon: ReactNode;
  onClick: () => void;
  disabled?: boolean;
  active?: boolean;
}) {
  return (
    <Tooltip content={label}>
      <Button
        aria-label={label}
        className={active ? "active-icon" : ""}
        icon={icon}
        theme="borderless"
        size="small"
        disabled={disabled}
        onClick={onClick}
      />
    </Tooltip>
  );
}
function useApi<T>(path: string | null, interval?: number) {
  return useQuery({
    queryKey: [path],
    queryFn: () => request<T>(path!),
    enabled: path !== null,
    refetchInterval: interval,
  });
}
function useAction<T>(
  action: (value: T) => Promise<unknown>,
  message = "操作成功",
) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: action,
    onSuccess: () => {
      Toast.success(message);
      void client.invalidateQueries();
    },
    onError: (error: Error) => Toast.error(error.message),
  });
}
function Loading({
  query,
}: {
  query: { isError: boolean; error: Error | null; refetch: () => unknown };
}) {
  return query.isError ? (
    <div className="empty-state">
      <CircleAlert size={30} />
      <h3>加载失败</h3>
      <p>{query.error?.message}</p>
      <Button
        icon={<RefreshCw size={16} />}
        onClick={() => void query.refetch()}
      >
        重试
      </Button>
    </div>
  ) : (
    <div className="loading">
      <Spin size="large" />
    </div>
  );
}
function Empty({
  icon = <Archive size={34} />,
  title,
  action,
}: {
  icon?: ReactNode;
  title: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty-state">
      {icon}
      <h3>{title}</h3>
      {action}
    </div>
  );
}
function PageTitle({
  title,
  count,
  children,
}: {
  title: string;
  count?: number;
  children?: ReactNode;
}) {
  return (
    <div className="page-title">
      <div>
        <h1>{title}</h1>
        {count !== undefined && (
          <span className="count">{count.toLocaleString()} 条记录</span>
        )}
      </div>
      <div className="title-actions">{children}</div>
    </div>
  );
}
function Pager({
  total,
  page,
  limit,
  onChange,
}: {
  total: number;
  page: number;
  limit: number;
  onChange: (page: number) => void;
}) {
  return total > limit ? (
    <div className="pagination">
      <Pagination
        total={total}
        currentPage={page}
        pageSize={limit}
        onPageChange={onChange}
        showSizeChanger={false}
      />
    </div>
  ) : null;
}
function CookieNotice({ cookie }: { cookie?: CookieStatus }) {
  return cookie && cookie.status !== "valid" ? (
    <Banner
      className="cookie-banner"
      type={cookie.status === "invalid" ? "danger" : "warning"}
      closeIcon={null}
      description={
        <span>
          <strong>Cookie {statusNames[cookie.status]}</strong>
          <span className="banner-reason">{cookie.reason}</span>
          <Link to="/settings">
            前往设置 <ArrowUpRight size={13} />
          </Link>
        </span>
      }
    />
  ) : null;
}

export default function App() {
  const client = useQueryClient();
  const session = useApi<{ authenticated: boolean; required: boolean }>(
    "/session",
  );
  const [password, setPassword] = useState("");
  const login = useAction(() => send("/session", { password }), "已登录");
  useEffect(() => {
    const handler = () =>
      void client.invalidateQueries({ queryKey: ["/session"] });
    window.addEventListener("session-expired", handler);
    return () => window.removeEventListener("session-expired", handler);
  }, [client]);
  if (!session.data) return <Loading query={session} />;
  if (!session.data.authenticated)
    return (
      <main className="login-page">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            login.mutate();
          }}
        >
          <Archive size={38} />
          <h1>X Archive</h1>
          <p>登录归档空间</p>
          <label htmlFor="password">访问密码</label>
          <Input
            id="password"
            type="password"
            value={password}
            onChange={setPassword}
            autoComplete="current-password"
          />
          <Button
            block
            htmlType="submit"
            theme="solid"
            loading={login.isPending}
            icon={<LockKeyhole size={16} />}
          >
            登录
          </Button>
          {login.error && <p className="error-text">{login.error.message}</p>}
        </form>
      </main>
    );
  return <Shell canLogout={session.data.required} />;
}

function Shell({ canLogout }: { canLogout: boolean }) {
  const cookie = useApi<CookieStatus>("/cookies", 15000);
  const dashboard = useApi<Dashboard>("/dashboard", 5000);
  const setImportOpen = useUI((s) => s.setImportOpen);
  const logout = useAction(
    () => send("/session", undefined, "DELETE"),
    "已退出",
  );
  const navigation = [
    ["/", "首页", LayoutDashboard],
    ["/users", "用户归档", Users],
    ["/relationships", "关注关系", Users],
    ["/tweets", "推文归档", Archive],
    ["/media", "媒体库", Image],
    ["/tasks", "下载任务", Download],
    ["/settings", "设置", SettingsIcon],
  ] as const;
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Link className="brand" to="/">
          <span className="brand-symbol">
            X<span />
          </span>
          <span>
            X Archive<small>个人归档空间</small>
          </span>
        </Link>
        <nav aria-label="主导航">
          {navigation.map(([path, label, Icon]) => (
            <NavLink
              key={path}
              to={path}
              end={path === "/"}
              className={({ isActive }) =>
                isActive ? "nav-link selected" : "nav-link"
              }
            >
              <Icon size={18} />
              <span>{label}</span>
              {path === "/tasks" && !!dashboard.data?.active_tasks && (
                <b className="nav-count">{dashboard.data.active_tasks}</b>
              )}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <span className="connection-dot" />
          本地归档<span>V{version}</span>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <span className="workspace-name">
            <FolderOpen size={16} /> 我的归档
          </span>
          <div className="topbar-actions">
            <Link to="/settings" className="session-status">
              <span
                className={`session-dot ${cookie.data?.status === "valid" ? "ok" : ""}`}
              />
              Cookie {statusNames[cookie.data?.status || "unknown"]}
            </Link>
            {canLogout && (
              <IconButton
                label="退出登录"
                icon={<LogOut size={16} />}
                onClick={() => logout.mutate()}
              />
            )}
            <Button
              theme="solid"
              icon={<Plus size={16} />}
              onClick={() => setImportOpen(true)}
            >
              导入推文
            </Button>
          </div>
        </header>
        <main className="main-content">
          <Routes>
            <Route path="/" element={<Home />} />
            <Route path="/users" element={<UsersPage />} />
            <Route path="/users/:id" element={<UserDetail />} />
            <Route path="/relationships" element={<RelationshipsPage />} />
            <Route path="/tweets" element={<TweetsPage />} />
            <Route path="/media" element={<MediaPage />} />
            <Route path="/tasks" element={<TasksPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route
              path="*"
              element={
                <Empty
                  title="页面不存在"
                  action={<Link to="/">返回首页</Link>}
                />
              }
            />
          </Routes>
        </main>
        <footer className="app-footer">
          <span>X ARCHIVE</span>
          <span>你的内容，本地留存</span>
        </footer>
      </div>
      <ImportModal />
    </div>
  );
}

function ImportModal() {
  const { importOpen, setImportOpen } = useUI();
  const [target, setTarget] = useState("");
  const navigate = useNavigate();
  const mutation = useAction(
    () => send("/tweets", { target }),
    "推文归档任务已加入队列",
  );
  async function submit() {
    await mutation.mutateAsync();
    setImportOpen(false);
    setTarget("");
    navigate("/tasks");
  }
  return (
    <Modal
      title="导入单条推文"
      visible={importOpen}
      onCancel={() => setImportOpen(false)}
      onOk={() => void submit().catch(() => {})}
      confirmLoading={mutation.isPending}
      okText="开始归档"
      okButtonProps={{ "aria-label": "开始归档" }}
      cancelText="取消"
      cancelButtonProps={{ "aria-label": "取消" }}
      width={520}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void submit().catch(() => {});
        }}
      >
        <label className="field-label" htmlFor="tweet-url">
          推文链接
        </label>
        <Input
          id="tweet-url"
          autoFocus
          value={target}
          onChange={setTarget}
          placeholder="https://x.com/username/status/..."
        />
        {mutation.error && (
          <p className="error-text">{mutation.error.message}</p>
        )}
      </form>
    </Modal>
  );
}

function Home() {
  const q = useApi<Dashboard>("/dashboard", 5000);
  const media = useApi<Page<Media>>("/media?limit=12", 5000);
  const tasks = useApi<Page<Task>>("/tasks?grouped=true&limit=5", 3000);
  const setImportOpen = useUI((s) => s.setImportOpen);
  if (!q.data) return <Loading query={q} />;
  const d = q.data;
  return (
    <>
      <PageTitle title="归档概览">
        <span className="today">
          {new Date().toLocaleDateString("zh-CN", {
            month: "long",
            day: "numeric",
            weekday: "long",
          })}
        </span>
      </PageTitle>
      <CookieNotice cookie={d.cookie} />
      <div className="stats-band">
        {[
          [Users, "归档用户", d.users, "/users"],
          [Archive, "已存推文", d.tweets, "/tweets"],
          [Image, "已下载媒体", d.media, "/media"],
          [Download, "进行中任务", d.active_tasks, "/tasks"],
        ].map(([Icon, label, value, link]) => {
          const Component = Icon as typeof Users;
          return (
            <Link to={link as string} className="stat" key={label as string}>
              <span>
                <Component size={17} />
                {label as string}
              </span>
              <strong>{(value as number).toLocaleString()}</strong>
              <ChevronRight size={16} />
            </Link>
          );
        })}
      </div>
      <section className="section">
        <div className="section-heading">
          <h2>最近归档</h2>
          <Link to="/media">
            全部媒体 <ArrowUpRight size={15} />
          </Link>
        </div>
        {!media.data ? (
          <Loading query={media} />
        ) : media.data.items.length ? (
          <MediaGrid items={media.data.items.slice(0, 8)} />
        ) : (
          <Empty
            icon={<Image size={36} />}
            title="尚无归档媒体"
            action={
              <Button
                icon={<Plus size={16} />}
                onClick={() => setImportOpen(true)}
              >
                导入推文
              </Button>
            }
          />
        )}
      </section>
      <div className="overview-bottom">
        <section className="section">
          <div className="section-heading">
            <h2>最近任务</h2>
            <Link to="/tasks">
              任务列表 <ArrowUpRight size={15} />
            </Link>
          </div>
          {!tasks.data ? (
            <Loading query={tasks} />
          ) : tasks.data.items.length ? (
            tasks.data.items.map((task) => (
              <div className="recent-task" key={task.id}>
                <span className="task-icon">
                  <Download size={17} />
                </span>
                <div>
                  <strong>{task.label}</strong>
                  <span>{date(task.created_at, true)}</span>
                </div>
                <Status value={task.status} />
              </div>
            ))
          ) : (
            <div className="muted-empty">暂无任务</div>
          )}
        </section>
        <section className="section storage-section">
          <div className="section-heading">
            <h2>存储空间</h2>
            <FolderOpen size={17} />
          </div>
          <div className="storage-number">
            {bytes(d.storage.media_bytes)}
            <small>已归档媒体</small>
          </div>
          <Progress
            percent={
              d.storage.total_bytes
                ? Math.min(
                    100,
                    Math.round(
                      (1 - d.storage.free_bytes / d.storage.total_bytes) * 100,
                    ),
                  )
                : 0
            }
            showInfo={false}
            stroke="#7fc9d6"
          />
          <div className="storage-meta">
            <span>磁盘可用 {bytes(d.storage.free_bytes)}</span>
            <span>总计 {bytes(d.storage.total_bytes)}</span>
          </div>
          <Link to="/settings">
            存储设置 <ChevronRight size={14} />
          </Link>
        </section>
      </div>
    </>
  );
}

function UsersPage() {
  const q = useApi<User[]>("/users", 5000);
  const [open, setOpen] = useState(false);
  const [target, setTarget] = useState("");
  const [search, setSearch] = useState("");
  const add = useAction(
    () => send("/users", { target }),
    "用户归档已启用，同步已排队",
  );
  const sync = useAction(
    (id: string) => send(`/users/${id}/sync`),
    "同步已加入队列",
  );
  const toggle = useAction(
    (u: User) =>
      send(
        `/users/${u.id}`,
        { enabled: !u.enabled, sync_interval: u.sync_interval },
        "PATCH",
      ),
    "归档状态已更新",
  );
  const remove = useAction(
    (id: string) => send(`/users/${id}`, undefined, "DELETE"),
    "已移除用户，推文与媒体已保留",
  );
  const items = q.data?.filter((u) =>
    `${u.username} ${u.name}`.toLowerCase().includes(search.toLowerCase()),
  );
  return (
    <>
      <PageTitle title="用户归档" count={q.data?.length}>
        <Button
          theme="solid"
          icon={<Plus size={16} />}
          onClick={() => setOpen(true)}
        >
          添加用户
        </Button>
      </PageTitle>
      <div className="toolbar">
        <Input
          prefix={<Search size={16} />}
          value={search}
          onChange={setSearch}
          placeholder="搜索用户"
          showClear
          style={{ width: 280 }}
        />
      </div>
      {!items ? (
        <Loading query={q} />
      ) : !items.length ? (
        <Empty
          icon={<Users size={34} />}
          title={search ? "没有匹配的用户" : "尚未添加归档用户"}
          action={
            !search && (
              <Button icon={<Plus size={16} />} onClick={() => setOpen(true)}>
                添加用户
              </Button>
            )
          }
        />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>用户</th>
                <th>归档内容</th>
                <th>最近同步</th>
                <th>自动同步</th>
                <th className="right">操作</th>
              </tr>
            </thead>
            <tbody>
              {items.map((u) => (
                <tr key={u.id}>
                  <td>
                    <Link className="user-identity" to={`/users/${u.id}`}>
                      <Avatar name={u.username} />
                      <div>
                        <strong>{u.name || u.username}</strong>
                        <span>@{u.username}</span>
                      </div>
                    </Link>
                    {u.last_error && (
                      <p className="row-error">{u.last_error}</p>
                    )}
                  </td>
                  <td>
                    <span className="tabular">{u.tweet_count}</span> 推文{" "}
                    <span className="separator">/</span>{" "}
                    <span className="tabular">{u.media_count}</span> 媒体
                  </td>
                  <td className="muted">{date(u.last_sync)}</td>
                  <td>
                    <Switch
                      size="small"
                      aria-label={`自动同步 ${u.username}`}
                      checked={u.enabled}
                      onChange={() => toggle.mutate(u)}
                      disabled={toggle.isPending}
                    />
                  </td>
                  <td>
                    <div className="row-actions">
                      <IconButton
                        label={`同步 ${u.username}`}
                        icon={<RefreshCw size={16} />}
                        onClick={() => sync.mutate(u.id)}
                        disabled={!u.enabled || sync.isPending}
                      />
                      <Link to={`/users/${u.id}`}>
                        <Button
                          theme="borderless"
                          size="small"
                          aria-label={`查看 ${u.username}`}
                          icon={<ChevronRight size={16} />}
                        />
                      </Link>
                      <IconButton
                        label={`移除 ${u.username}`}
                        icon={<Trash2 size={16} />}
                        onClick={() =>
                          Modal.confirm({
                            title: `移除 @${u.username}？`,
                            content:
                              "该用户将停止同步，已归档的推文和媒体会保留。关注快照将一并移除。",
                            okText: "移除",
                            cancelText: "取消",
                            onOk: () => remove.mutateAsync(u.id),
                          })
                        }
                      />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Modal
        title="添加归档用户"
        visible={open}
        onCancel={() => setOpen(false)}
        okText="添加并同步"
        okButtonProps={{ "aria-label": "添加并同步" }}
        cancelText="取消"
        cancelButtonProps={{ "aria-label": "取消" }}
        confirmLoading={add.isPending}
        onOk={() => {
          void add
            .mutateAsync()
            .then(() => {
              setOpen(false);
              setTarget("");
            })
            .catch(() => {});
        }}
        width={500}
      >
        <label className="field-label" htmlFor="user-target">
          用户名或主页链接
        </label>
        <Input
          id="user-target"
          value={target}
          onChange={setTarget}
          placeholder="@username 或 https://x.com/username"
        />
        {add.error && <p className="error-text">{add.error.message}</p>}
      </Modal>
    </>
  );
}

function Avatar({ name }: { name: string }) {
  return (
    <span className="avatar" aria-hidden="true">
      {name.slice(0, 2).toUpperCase()}
    </span>
  );
}
function UserDetail() {
  const { id } = useParams();
  const q = useApi<User>(`/users/${id}`, 5000);
  const [interval, setIntervalValue] = useState<number | undefined>();
  const sync = useAction(() => send(`/users/${id}/sync`), "同步已加入队列");
  const save = useAction(
    () =>
      send(
        `/users/${id}`,
        { enabled: q.data?.enabled, sync_interval: interval || null },
        "PATCH",
      ),
    "同步间隔已保存",
  );
  useEffect(() => {
    if (q.data) setIntervalValue(q.data.sync_interval || undefined);
  }, [q.data?.sync_interval]);
  if (!q.data) return <Loading query={q} />;
  const u = q.data;
  return (
    <>
      <Link className="back-link" to="/users">
        <ArrowLeft size={16} />
        用户归档
      </Link>
      <div className="profile-heading">
        <Avatar name={u.username} />
        <div>
          <h1>{u.name || u.username}</h1>
          <span>@{u.username}</span>
        </div>
        <a
          href={`https://x.com/${u.username}`}
          target="_blank"
          rel="noreferrer"
        >
          <Button icon={<ExternalLink size={15} />}>X 主页</Button>
        </a>
        <Button
          theme="solid"
          icon={<RefreshCw size={16} />}
          disabled={!u.enabled}
          loading={sync.isPending}
          onClick={() => sync.mutate()}
        >
          立即同步
        </Button>
      </div>
      {u.bio && <p className="profile-bio">{u.bio}</p>}
      {u.last_error && (
        <Banner type="danger" description={u.last_error} closeIcon={null} />
      )}
      <div className="profile-metrics">
        <span>
          <strong>{u.tweet_count}</strong> 推文
        </span>
        <span>
          <strong>{u.media_count}</strong> 媒体
        </span>
        <span>最近同步 {date(u.last_sync)}</span>
        <Status value={u.enabled ? "enabled" : "paused"} />
      </div>
      <div className="profile-config">
        <label>同步间隔</label>
        <InputNumber
          aria-label="用户同步间隔"
          value={interval}
          onChange={(value) =>
            setIntervalValue(typeof value === "number" ? value : undefined)
          }
          min={5}
          max={43200}
          placeholder="使用默认间隔"
          suffix="分钟"
          style={{ width: 190 }}
        />
        <Button
          icon={<Check size={15} />}
          loading={save.isPending}
          onClick={() => save.mutate()}
        >
          保存
        </Button>
        <Link to={`/relationships?user=${id}`}>
          关注关系 <ArrowUpRight size={14} />
        </Link>
      </div>
      <div className="section-heading">
        <h2>归档媒体</h2>
        <Link to={`/tweets?user=${id}`}>
          全部推文 <ArrowUpRight size={14} />
        </Link>
      </div>
      <MediaPage embedded userId={id} />
    </>
  );
}

function TweetsPage() {
  const [sp, setSp] = useSearchParams();
  const [search, setSearch] = useState(sp.get("search") || "");
  const [detail, setDetail] = useState<Media | null>(null);
  const page = Number(sp.get("page") || 1);
  const users = useApi<User[]>("/users");
  const q = useApi<Page<Tweet>>(
    `/tweets?${params({ page, user: sp.get("user") || undefined, search: sp.get("search") || undefined, limit: 30 })}`,
    5000,
  );
  const setImportOpen = useUI((s) => s.setImportOpen);
  function filter(key: string, value: string) {
    const next = new URLSearchParams(sp);
    value ? next.set(key, value) : next.delete(key);
    next.delete("page");
    setSp(next);
  }
  return (
    <>
      <PageTitle title="推文归档" count={q.data?.total}>
        <Button
          theme="solid"
          icon={<Plus size={16} />}
          onClick={() => setImportOpen(true)}
        >
          单条导入
        </Button>
      </PageTitle>
      <div className="toolbar">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            filter("search", search);
          }}
        >
          <Input
            prefix={<Search size={16} />}
            value={search}
            onChange={setSearch}
            placeholder="搜索正文或作者"
            showClear
            style={{ width: 280 }}
          />
        </form>
        <Select
          aria-label="筛选用户"
          placeholder="全部用户"
          value={sp.get("user") || ""}
          optionList={[
            { value: "", label: "全部用户" },
            ...(users.data || []).map((u) => ({
              value: u.id,
              label: `@${u.username}`,
            })),
          ]}
          onChange={(value) => filter("user", String(value))}
          style={{ width: 190 }}
        />
      </div>
      {!q.data ? (
        <Loading query={q} />
      ) : !q.data.items.length ? (
        <Empty
          title="没有匹配的归档推文"
          action={
            <Button
              icon={<Plus size={16} />}
              onClick={() => setImportOpen(true)}
            >
              导入推文
            </Button>
          }
        />
      ) : (
        <div className="tweet-list">
          {q.data.items.map((t) => (
            <article className="tweet-row" key={t.id}>
              <Avatar name={t.author} />
              <div className="tweet-content">
                <div className="tweet-meta">
                  <strong>{t.author_name || t.author}</strong>
                  <span>@{t.author}</span>
                  <time>{date(t.published_at)}</time>
                </div>
                <p>{t.text || "无正文"}</p>
                {t.media.length > 0 && (
                  <div
                    className={`tweet-media ${t.media.length === 1 ? "single" : t.media.length === 3 ? "three" : ""}`}
                  >
                    {t.media.map((m) => (
                      <MediaPreview
                        key={m.id}
                        item={m}
                        onClick={() => setDetail(m)}
                      />
                    ))}
                  </div>
                )}
                <div className="tweet-actions">
                  {t.media_count > 0 && (
                    <Link to={`/media?tweet=${t.id}`}>
                      <Image size={14} />
                      {t.ready_count} / {t.media_count} 媒体已下载
                    </Link>
                  )}
                  <a href={t.url} target="_blank" rel="noreferrer">
                    原始推文 <ArrowUpRight size={14} />
                  </a>
                </div>
              </div>
            </article>
          ))}
        </div>
      )}
      {q.data && (
        <Pager
          {...q.data}
          onChange={(page) => {
            const next = new URLSearchParams(sp);
            next.set("page", String(page));
            setSp(next);
          }}
        />
      )}
      {detail && (
        <MediaDetail
          item={
            q.data?.items
              .flatMap((t) => t.media)
              .find((m) => m.id === detail.id) || detail
          }
          items={
            q.data?.items.find((t) => t.id === detail.tweet_id)?.media || [detail]
          }
          onSelect={setDetail}
          onClose={() => setDetail(null)}
        />
      )}
    </>
  );
}

function MediaPage({
  embedded = false,
  userId,
}: {
  embedded?: boolean;
  userId?: string;
}) {
  const [sp, setSp] = useSearchParams();
  const users = useApi<User[]>("/users");
  const tags = useApi<string[]>("/tags");
  const kind = sp.get("kind") || "";
  const compact = useUI((s) => s.compact);
  const setCompact = useUI((s) => s.setCompact);
  const page = Number(sp.get("page") || 1);
  const q = useApi<Page<Media>>(
    `/media?${params({ page, limit: 48, user: userId || sp.get("user") || undefined, tweet: sp.get("tweet") || undefined, kind: kind || undefined, from: sp.get("from") || undefined, to: sp.get("to") || undefined, favorite: sp.get("favorite") || undefined, tag: sp.get("tag") || undefined })}`,
    5000,
  );
  function filter(key: string, value: string) {
    const next = new URLSearchParams(sp);
    value ? next.set(key, value) : next.delete(key);
    next.delete("page");
    setSp(next);
  }
  return (
    <>
      {!embedded && (
        <PageTitle title="媒体库" count={q.data?.total}>
          <Tooltip content="紧凑排列">
            <Switch
              aria-label="紧凑排列"
              checked={compact}
              onChange={setCompact}
              size="small"
            />
          </Tooltip>
        </PageTitle>
      )}
      <div className="toolbar media-toolbar">
        <div className="segmented" aria-label="媒体类型">
          {[
            ["", "全部"],
            ["image", "图片"],
            ["video", "视频"],
            ["gif", "GIF"],
          ].map(([value, label]) => (
            <button
              key={label}
              className={kind === value ? "chosen" : ""}
              onClick={() => filter("kind", value)}
            >
              {label}
            </button>
          ))}
        </div>
        {!userId && (
          <Select
            aria-label="媒体用户"
            value={sp.get("user") || ""}
            optionList={[
              { value: "", label: "全部用户" },
              ...(users.data || []).map((u) => ({
                value: u.id,
                label: `@${u.username}`,
              })),
            ]}
            onChange={(v) => filter("user", String(v))}
            style={{ width: 160 }}
          />
        )}
        <Select
          aria-label="标签筛选"
          value={sp.get("tag") || ""}
          optionList={[
            { value: "", label: "全部标签" },
            ...(tags.data || []).map((t) => ({ value: t, label: t })),
          ]}
          onChange={(v) => filter("tag", String(v))}
          style={{ width: 140 }}
        />
        <label className="date-filter">
          <input
            aria-label="开始日期"
            type="date"
            value={sp.get("from") || ""}
            onChange={(e) => filter("from", e.target.value)}
          />
          <span>至</span>
          <input
            aria-label="结束日期"
            type="date"
            value={sp.get("to") || ""}
            onChange={(e) => filter("to", e.target.value)}
          />
        </label>
        <Tooltip content="仅看收藏">
          <Button
            aria-label="仅看收藏"
            icon={
              <Heart
                size={16}
                fill={sp.get("favorite") === "true" ? "currentColor" : "none"}
              />
            }
            theme={sp.get("favorite") ? "light" : "borderless"}
            onClick={() => filter("favorite", sp.get("favorite") ? "" : "true")}
          />
        </Tooltip>
        {sp.size > 0 && (
          <IconButton
            label="清除筛选"
            icon={<X size={16} />}
            onClick={() => setSp({})}
          />
        )}
      </div>
      {q.isError && q.data && (
        <Banner type="danger" description={q.error.message} closeIcon={null} />
      )}
      {!q.data ? (
        <Loading query={q} />
      ) : !q.data.items.length ? (
        <Empty icon={<Image size={36} />} title="没有匹配的媒体" />
      ) : (
        <MediaGrid items={q.data.items} compact={compact} />
      )}
      {q.data && (
        <Pager
          {...q.data}
          onChange={(page) => {
            const next = new URLSearchParams(sp);
            next.set("page", String(page));
            setSp(next);
          }}
        />
      )}
    </>
  );
}

function openPhoto(
  item: Media,
  items: Media[],
  onSelect: (item: Media) => void,
  onClose: () => void,
) {
  const images = items.filter(
    (m) => m.has_file && (m.kind === "image" || m.extension === "gif"),
  );
  const index = images.findIndex((m) => m.id === item.id);
  const lightbox = new PhotoSwipe({
    dataSource: images.map((m) => ({
      src: `/api/media/${m.id}/file`,
      width: m.width || 1200,
      height: m.height || 1200,
      alt: m.text || `@${m.author}`,
    })),
    index: Math.max(0, index),
    bgOpacity: 0.96,
    showHideAnimationType: "none",
    closeTitle: "关闭",
    zoomTitle: "缩放",
    arrowPrevTitle: "上一张",
    arrowNextTitle: "下一张",
  });
  lightbox.on("change", () => onSelect(images[lightbox.currIndex]));
  lightbox.on("destroy", onClose);
  lightbox.init();
  return lightbox;
}

function MediaPreview({
  item: m,
  onClick,
}: {
  item: Media;
  onClick: () => void;
}) {
  const video = m.kind !== "image" && m.extension !== "gif";
  return (
    <button
      className={`media-preview ${!m.has_file ? "not-ready" : ""}`}
      onClick={onClick}
      aria-label={`预览 @${m.author} 的${m.kind === "image" ? "图片" : m.kind === "gif" ? "GIF" : "视频"}`}
      style={
        {
          "--media-aspect": Math.max(
            0.6,
            Math.min(1.6, m.width && m.height ? m.width / m.height : 1),
          ),
        } as CSSProperties
      }
    >
      {m.has_file ? (
        <>
          {video && !m.has_thumbnail ? (
            <video
              src={`/api/media/${m.id}/file`}
              muted
              playsInline
              preload="metadata"
            />
          ) : (
            <img
              src={`/api/media/${m.id}/${m.extension === "gif" ? "file" : m.has_thumbnail ? "thumbnail" : "file"}`}
              alt={m.text || `@${m.author} 的媒体`}
              loading="lazy"
            />
          )}
          {video && (
            <span className="media-play">
              <Play size={23} fill="currentColor" />
            </span>
          )}
          {m.kind !== "image" && (
            <span className="media-kind">
              {m.kind === "gif" ? (
                "GIF"
              ) : (
                <>
                  <Video size={13} />
                  {m.duration
                    ? `${Math.floor(m.duration / 60)}:${String(Math.floor(m.duration % 60)).padStart(2, "0")}`
                    : "视频"}
                </>
              )}
            </span>
          )}
        </>
      ) : (
        <>
          <Image size={25} />
          <Status value={m.status} />
        </>
      )}
    </button>
  );
}

function MediaGrid({
  items,
  compact = false,
}: {
  items: Media[];
  compact?: boolean;
}) {
  const [detail, setDetail] = useState<Media | null>(null);
  useEffect(() => {
    if (detail && !items.some((item) => item.id === detail.id)) setDetail(null);
  }, [detail, items]);
  const favorite = useAction(
    (m: Media) => send(`/media/${m.id}`, { favorite: !m.favorite }, "PATCH"),
    "收藏已更新",
  );
  return (
    <>
      <div className={`media-grid ${compact ? "compact" : ""}`}>
        {items.map((m) => (
          <article className="media-card" key={m.id}>
            <MediaPreview item={m} onClick={() => setDetail(m)} />
            <div className="media-info">
              <div>
                <span>@{m.author}</span>
                <time>{date(m.published_at, true).split(" ")[0]}</time>
              </div>
              <div className="media-card-actions">
                <IconButton
                  label={m.favorite ? "取消收藏" : "收藏"}
                  icon={
                    <Heart
                      size={15}
                      fill={m.favorite ? "currentColor" : "none"}
                    />
                  }
                  active={m.favorite}
                  disabled={favorite.isPending}
                  onClick={() => favorite.mutate(m)}
                />
                <IconButton
                  label="媒体详情"
                  icon={<Info size={15} />}
                  onClick={() => setDetail(m)}
                />
              </div>
            </div>
            {m.tags && (
              <div className="media-tags">
                {m.tags
                  .split(",")
                  .slice(0, 3)
                  .map((tag) => (
                    <Tag key={tag} size="small">
                      {tag}
                    </Tag>
                  ))}
              </div>
            )}
          </article>
        ))}
      </div>
      {detail && (
        <MediaDetail
          item={items.find((i) => i.id === detail.id) || detail}
          items={items}
          onSelect={setDetail}
          onClose={() => setDetail(null)}
        />
      )}
    </>
  );
}

function MediaDetail({
  item: m,
  items,
  onSelect,
  onClose,
}: {
  item: Media;
  items: Media[];
  onSelect: (item: Media) => void;
  onClose: () => void;
}) {
  const index = items.findIndex((item) => item.id === m.id);
  const video = m.kind !== "image" && m.extension !== "gif";
  const [zoomed, setZoomed] = useState(false);
  const lightbox = useRef<PhotoSwipe | null>(null);
  const player = useRef<HTMLVideoElement>(null);
  const thumbnails = useRef<HTMLDivElement>(null);
  useEffect(() => () => lightbox.current?.destroy(), []);
  useEffect(() => {
    const element = player.current;
    if (element?.autoplay) void element.play().catch(() => {});
    return () => element?.pause();
  }, [m.id, m.has_file, video]);
  useEffect(() => {
    thumbnails.current
      ?.querySelector('[aria-current="true"]')
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [m.id]);
  useEffect(() => {
    function navigate(event: KeyboardEvent) {
      if (
        zoomed ||
        event.defaultPrevented ||
        event.altKey ||
        event.ctrlKey ||
        event.metaKey ||
        (event.target instanceof Element &&
          event.target.closest("input, textarea, select, [contenteditable], video"))
      )
        return;
      const next =
        event.key === "ArrowLeft"
          ? items[index - 1]
          : event.key === "ArrowRight"
            ? items[index + 1]
            : undefined;
      if (next) {
        event.preventDefault();
        onSelect(next);
      }
    }
    document.addEventListener("keydown", navigate);
    return () => document.removeEventListener("keydown", navigate);
  }, [index, items, onSelect, zoomed]);
  function zoom() {
    setZoomed(true);
    lightbox.current = openPhoto(m, items, onSelect, () => {
      lightbox.current = null;
      setZoomed(false);
    });
  }
  return (
    <Modal
      className="media-modal"
      title={`@${m.author} · ${m.kind === "image" ? "图片" : m.kind === "gif" ? "GIF" : "视频"}`}
      visible
      centered
      closeOnEsc={!zoomed}
      onCancel={onClose}
      footer={null}
      width="calc(100vw - 48px)"
    >
      <div className="media-detail">
        <div className="detail-viewer">
          <div className="detail-preview">
            {m.has_file ? (
              video ? (
                <video
                  key={m.id}
                  ref={player}
                  src={`/api/media/${m.id}/file`}
                  poster={
                    m.has_thumbnail ? `/api/media/${m.id}/thumbnail` : undefined
                  }
                  controls
                  playsInline
                  loop={m.kind === "gif"}
                  autoPlay={m.kind === "gif"}
                  muted={m.kind === "gif"}
                  preload="metadata"
                />
              ) : (
                <button
                  className="detail-image"
                  onClick={zoom}
                  aria-label="放大原图"
                >
                  <img
                    key={m.id}
                    src={`/api/media/${m.id}/file`}
                    alt={m.text || `@${m.author} 的媒体`}
                  />
                  <span className="detail-zoom">
                    <Maximize2 size={17} />
                  </span>
                </button>
              )
            ) : (
              <Empty title={statusNames[m.status] || m.status} />
            )}
          </div>
          {items.length > 1 && (
            <>
              <div className="detail-navigation">
                <IconButton
                  label="上一个媒体"
                  icon={<ChevronLeft size={20} />}
                  disabled={index <= 0}
                  onClick={() => onSelect(items[index - 1])}
                />
                <span aria-live="polite">{index + 1} / {items.length}</span>
                <IconButton
                  label="下一个媒体"
                  icon={<ChevronRight size={20} />}
                  disabled={index < 0 || index >= items.length - 1}
                  onClick={() => onSelect(items[index + 1])}
                />
              </div>
              <div
                className="detail-thumbnails"
                ref={thumbnails}
                aria-label="切换媒体"
              >
                {items.map((item, i) => (
                  <button
                    key={item.id}
                    className="detail-thumbnail"
                    aria-label={`切换到第 ${i + 1} 个媒体`}
                    aria-current={item.id === m.id ? "true" : undefined}
                    onClick={() => onSelect(item)}
                  >
                    {item.has_file &&
                    (item.has_thumbnail ||
                      item.kind === "image" ||
                      item.extension === "gif") ? (
                      <img
                        src={`/api/media/${item.id}/${item.has_thumbnail ? "thumbnail" : "file"}`}
                        alt=""
                        loading="lazy"
                      />
                    ) : item.kind === "image" ? (
                      <Image size={19} />
                    ) : (
                      <Video size={19} />
                    )}
                    {item.kind !== "image" && (
                      <span>
                        {item.kind === "gif" ? (
                          "GIF"
                        ) : (
                          <Play size={10} fill="currentColor" />
                        )}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            </>
          )}
        </div>
        <MediaMetadata key={m.id} item={m} />
      </div>
    </Modal>
  );
}

function MediaMetadata({ item: m }: { item: Media }) {
  const [draftTags, setDraftTags] = useState<string | null>(null);
  const tags = draftTags ?? m.tags;
  const save = useAction(
    () =>
      send(
        `/media/${m.id}`,
        {
          tags: tags
            .split(/[,，]/)
            .map((t) => t.trim())
            .filter(Boolean),
        },
        "PATCH",
      ),
    "标签已保存",
  );
  const download = useAction(
    () => send(`/media/${m.id}/download`),
    "下载已加入队列",
  );
  const favorite = useAction(
    () => send(`/media/${m.id}`, { favorite: !m.favorite }, "PATCH"),
    "收藏已更新",
  );
  return (
    <div className="detail-meta">
      <div className="detail-author">
        <Avatar name={m.author} />
        <div>
          <strong>@{m.author}</strong>
          <time>{date(m.published_at)}</time>
        </div>
      </div>
      <p className="detail-text">{m.text || "无正文"}</p>
      <a href={m.url} target="_blank" rel="noreferrer">
        查看原始推文 <ExternalLink size={14} />
      </a>
      <dl>
        <dt>状态</dt>
        <dd>
          <Status value={m.status} />
        </dd>
        <dt>尺寸</dt>
        <dd>
          {m.width} × {m.height}
        </dd>
        <dt>文件</dt>
        <dd>
          {m.extension.toUpperCase()} · {bytes(m.size)}
        </dd>
      </dl>
      {m.error && <p className="error-text">{m.error}</p>}
      <div className="detail-commands">
        <Button
          size="small"
          icon={<Heart size={15} fill={m.favorite ? "currentColor" : "none"} />}
          onClick={() => favorite.mutate()}
          loading={favorite.isPending}
        >
          {m.favorite ? "已收藏" : "收藏"}
        </Button>
        <Button
          size="small"
          icon={<ArrowDownToLine size={15} />}
          onClick={() => download.mutate()}
          loading={download.isPending}
        >
          {m.has_file ? "重新下载" : "下载"}
        </Button>
      </div>
      <label className="field-label" htmlFor="media-tags">
        <Tags size={14} />
        标签
      </label>
      <Input
        id="media-tags"
        size="small"
        value={tags}
        onChange={setDraftTags}
        disabled={save.isPending}
        placeholder="以逗号分隔"
      />
      <Button
        size="small"
        icon={<Check size={15} />}
        onClick={() =>
          save.mutate(undefined, { onSuccess: () => setDraftTags(null) })
        }
        loading={save.isPending}
      >
        保存标签
      </Button>
    </div>
  );
}

function TasksPage() {
  const [sp, setSp] = useSearchParams();
  const status = sp.get("status") || "";
  const page = Number(sp.get("page") || 1);
  const q = useApi<Page<TaskGroup>>(
    `/tasks?${params({ grouped: true, status: status || undefined, page, limit: 30 })}`,
    2000,
  );
  const retry = useAction(
    (id: string) => send(`/tasks/${id}/retry`),
    "重试任务已排队",
  );
  const cancel = useAction(
    (id: string) => send(`/tasks/${id}/cancel`),
    "正在取消任务",
  );
  return (
    <>
      <PageTitle title="下载任务" count={q.data?.total}>
        <IconButton
          label="刷新任务"
          icon={<RefreshCw size={17} />}
          onClick={() => void q.refetch()}
        />
      </PageTitle>
      <div className="toolbar">
        <Select
          aria-label="任务状态"
          value={status}
          optionList={[
            { value: "", label: "全部状态" },
            ...[
              "queued",
              "running",
              "cancelling",
              "succeeded",
              "failed",
              "cancelled",
            ].map((value) => ({ value, label: statusNames[value] })),
          ]}
          onChange={(v) => setSp(v ? { status: String(v) } : {})}
          style={{ width: 180 }}
        />
        <span className="muted task-summary">
          <Clock3 size={14} />
          自动刷新
        </span>
      </div>
      {!q.data ? (
        <Loading query={q} />
      ) : !q.data.items.length ? (
        <Empty icon={<Download size={34} />} title="暂无任务" />
      ) : (
        <div className="table-wrap task-table-wrap">
          <table className="task-table" aria-label="下载任务">
            <thead>
              <tr>
                <th>任务</th>
                <th>状态</th>
                <th>下载 / 大小</th>
                <th>结果</th>
                <th>创建时间</th>
                <th className="right">操作</th>
              </tr>
            </thead>
            <tbody>
              {q.data.items.map((t) => (
                <tr className="task-row" key={t.id}>
                  <td className="task-name-cell">
                    <strong>{t.label}</strong>
                    <div className="task-facts">
                      <span>
                        {({
                          tweet: "推文归档",
                          sync: "用户同步",
                          relationships: "关系快照",
                          cookie_check: "会话检测",
                          download: t.download_total > 0 ? "媒体下载" : "手动下载",
                        } as Record<string, string>)[t.kind] || t.kind}
                      </span>
                      <span>尝试 {t.attempts} / {t.max_attempts}</span>
                    </div>
                  </td>
                  <td className="task-status-cell">
                    <Status value={t.status} />
                    {t.status === "running" && (
                      <div className="task-progress">
                        <Progress percent={t.progress} showInfo={false} />
                        <span>{t.progress}%</span>
                      </div>
                    )}
                  </td>
                  <td className="task-download-cell">
                    {t.download_total > 0 ? (
                      <>
                        <span>{t.download_completed} / {t.download_total} 完成</span>
                        <small>{bytes(t.download_size)}</small>
                      </>
                    ) : t.kind === "download" && t.result_size > 0 ? (
                      bytes(t.result_size)
                    ) : (
                      <span className="muted">-</span>
                    )}
                  </td>
                  <td className="task-result-cell">
                    {(t.error || t.download_error) && (
                      <p className={t.status === "cancelled" ? "muted" : "task-error"}>
                        {t.error || t.download_error}
                        {t.status === "queued" && (
                          <span> · 下次尝试 {date(t.available_at, true)}</span>
                        )}
                      </p>
                    )}
                    {(t.download_failed > 0 || t.download_cancelled > 0) && (
                      <p className="task-error">
                        {[
                          t.download_failed > 0 && `${t.download_failed} 个下载失败`,
                          t.download_cancelled > 0 && `${t.download_cancelled} 个已取消`,
                        ].filter(Boolean).join("，")}
                      </p>
                    )}
                    {t.result && (
                      <p className="task-result">
                        {t.result.replace(/(\d+)\s*字节/g, (_, size: string) => bytes(Number(size)))}
                      </p>
                    )}
                    {!t.result && !t.error && !t.download_error && (
                      <span className="muted">-</span>
                    )}
                  </td>
                  <td className="task-time-cell">
                    <time dateTime={t.created_at}>{date(t.created_at, true)}</time>
                  </td>
                  <td className="task-controls">
                    {["failed", "cancelled"].includes(t.status) && (
                      <IconButton
                        label="重试任务"
                        icon={<RefreshCw size={15} />}
                        onClick={() => retry.mutate(t.id)}
                        disabled={retry.isPending}
                      />
                    )}
                    {["queued", "running"].includes(t.status) && (
                      <IconButton
                        label="取消任务"
                        icon={<X size={15} />}
                        onClick={() => cancel.mutate(t.id)}
                        disabled={cancel.isPending}
                      />
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.data && (
        <Pager
          {...q.data}
          onChange={(page) =>
            setSp({ ...(status ? { status } : {}), page: String(page) })
          }
        />
      )}
    </>
  );
}

function RelationshipsPage() {
  const users = useApi<User[]>("/users", 5000);
  const [sp, setSp] = useSearchParams();
  const user = sp.get("user") || users.data?.[0]?.id || "";
  const direction = sp.get("direction") || "following";
  const snapshots = useApi<Snapshot[]>(
    user ? `/users/${user}/snapshots?direction=${direction}` : null,
    5000,
  );
  const [selectedId, setSelected] = useState("");
  const selected =
    snapshots.data?.find((s) => s.id === selectedId) || snapshots.data?.[0];
  const [memberPage, setMemberPage] = useState(1);
  const members = useApi<Page<Member> & { snapshot: Snapshot }>(
    selected ? `/snapshots/${selected.id}?page=${memberPage}&limit=50` : null,
  );
  const [base, setBase] = useState("");
  const [compare, setCompare] = useState(false);
  const diff = useApi<{ added: Member[]; removed: Member[] }>(
    compare && selected && base
      ? `/snapshots/${selected.id}/diff?base=${base}`
      : null,
  );
  const capture = useAction(
    () => send(`/users/${user}/snapshots`, { direction }),
    "关系快照已加入队列",
  );
  useEffect(() => {
    setSelected("");
    setBase("");
    setCompare(false);
    setMemberPage(1);
  }, [user, direction]);
  if (!users.data) return <Loading query={users} />;
  return (
    <>
      <PageTitle title="关注关系">
        <Button
          theme="solid"
          icon={<Plus size={16} />}
          disabled={!user || !users.data.find((u) => u.id === user)?.enabled}
          loading={capture.isPending}
          onClick={() => capture.mutate()}
        >
          创建快照
        </Button>
      </PageTitle>
      {!users.data.length ? (
        <Empty
          icon={<Users size={34} />}
          title="尚无归档用户"
          action={
            <Link to="/users">
              <Button icon={<Plus size={16} />}>添加用户</Button>
            </Link>
          }
        />
      ) : (
        <>
          <div className="toolbar">
            <Select
              aria-label="关系用户"
              value={user}
              optionList={users.data.map((u) => ({
                value: u.id,
                label: `@${u.username}`,
              }))}
              onChange={(v) => setSp({ user: String(v), direction })}
              style={{ width: 210 }}
            />
            <div className="segmented">
              {[
                ["following", "Following"],
                ["followers", "Followers"],
              ].map(([value, label]) => (
                <button
                  key={value}
                  className={direction === value ? "chosen" : ""}
                  onClick={() => setSp({ user, direction: value })}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
          {!snapshots.data ? (
            <Loading query={snapshots} />
          ) : !snapshots.data.length ? (
            <Empty
              icon={<Users size={34} />}
              title="尚无关系快照"
              action={
                <Button
                  icon={<Plus size={16} />}
                  disabled={!users.data.find(u => u.id === user)?.enabled}
                  onClick={() => capture.mutate()}
                  loading={capture.isPending}
                >
                  创建快照
                </Button>
              }
            />
          ) : (
            <div className="relationships-layout">
              <div className="snapshot-history">
                <h2>快照历史</h2>
                {snapshots.data.map((s) => (
                  <button
                    key={s.id}
                    className={`snapshot-item ${selected?.id === s.id ? "chosen" : ""}`}
                    onClick={() => {
                      setSelected(s.id);
                      setBase("");
                      setCompare(false);
                      setMemberPage(1);
                    }}
                  >
                    <time>{date(s.created_at)}</time>
                    <span>
                      <Status value={s.status} />
                      <b>
                        {s.fetched_count} / {s.expected_count ?? "未知"}
                      </b>
                    </span>
                  </button>
                ))}
              </div>
              <div className="snapshot-content">
                {selected && (
                  <>
                    <div className="section-heading">
                      <h2>
                        {date(selected.created_at)}
                        <Status value={selected.status} />
                      </h2>
                      <span className="muted">
                        {selected.fetched_count} 条关系
                      </span>
                    </div>
                    {selected.status !== "complete" && (
                      <Banner
                        type="warning"
                        closeIcon={null}
                        description={
                          selected.error ||
                          "此快照未确认完整，不参与新增或取消关系判断"
                        }
                      />
                    )}
                    <div className="snapshot-compare">
                      <span id="snapshot-baseline-label" hidden>
                        对比基准快照
                      </span>
                      <Select
                        aria-labelledby="snapshot-baseline-label"
                        placeholder="选择基准快照"
                        value={base || undefined}
                        optionList={snapshots.data
                          .filter(
                            (s) =>
                              s.id !== selected.id &&
                              s.status === "complete" &&
                              s.created_at < selected.created_at,
                          )
                          .map((s) => ({
                            value: s.id,
                            label: `${date(s.created_at)} · ${s.fetched_count} 条`,
                          }))}
                        onChange={(v) => {
                          setBase(String(v));
                          setCompare(false);
                        }}
                        style={{ width: 270 }}
                      />
                      <Button
                        icon={<ListFilter size={16} />}
                        disabled={selected.status !== "complete" || !base}
                        onClick={() => setCompare(true)}
                      >
                        查看差异
                      </Button>
                      {compare && (
                        <Button
                          theme="borderless"
                          onClick={() => setCompare(false)}
                        >
                          查看全部
                        </Button>
                      )}
                    </div>
                    {compare ? (
                      !diff.data ? (
                        <Loading query={diff} />
                      ) : (
                        <div className="diff-columns">
                          <MemberList
                            title={`新增 (${diff.data.added.length})`}
                            members={diff.data.added}
                          />
                          <MemberList
                            title={`取消 (${diff.data.removed.length})`}
                            members={diff.data.removed}
                          />
                        </div>
                      )
                    ) : !members.data ? (
                      <Loading query={members} />
                    ) : (
                      <>
                        <MemberList members={members.data.items} />
                        <Pager {...members.data} onChange={setMemberPage} />
                      </>
                    )}
                  </>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </>
  );
}
function MemberList({ members, title }: { members: Member[]; title?: string }) {
  return (
    <div className="member-list">
      {title && <h3>{title}</h3>}
      {members.length ? (
        members.map((m) => (
          <a
            href={`https://x.com/${m.username}`}
            target="_blank"
            rel="noreferrer"
            className="member-row"
            key={m.id}
          >
            <Avatar name={m.username} />
            <div>
              <strong>{m.name || m.username}</strong>
              <span>@{m.username}</span>
            </div>
            <ArrowUpRight size={15} />
          </a>
        ))
      ) : (
        <div className="muted-empty">暂无记录</div>
      )}
    </div>
  );
}

function SettingsPage() {
  const q = useApi<SettingsResponse>("/settings", 15000);
  const [authToken, setAuthToken] = useState("");
  const [ct0, setCt0] = useState("");
  const [draft, setDraft] = useState<Settings | null>(null);
  const [dirty, setDirty] = useState(false);
  const save = useAction(() => send("/settings", draft, "PUT"), "设置已保存");
  const saveCookie = useAction(
    () =>
      send("/cookies", {
        cookie: `auth_token=${authToken.trim()}; ct0=${ct0.trim()}`,
      }),
    "Cookie 已保存，检测已排队",
  );
  const check = useAction(
    () => send("/cookies/check"),
    "Cookie 检测已加入队列",
  );
  const remove = useAction(
    () => send("/cookies", undefined, "DELETE"),
    "Cookie 已删除",
  );
  useEffect(() => {
    if (q.data && !dirty) setDraft(q.data.settings);
  }, [q.data, dirty]);
  if (!q.data || !draft) return <Loading query={q} />;
  const data = q.data;
  function change<K extends keyof Settings>(key: K, value: Settings[K]) {
    setDraft((d) => d && { ...d, [key]: value });
    setDirty(true);
  }
  return (
    <>
      <PageTitle title="设置">
        <Button
          theme="solid"
          icon={<Check size={16} />}
          loading={save.isPending}
          disabled={!dirty}
          onClick={() => {
            void save
              .mutateAsync()
              .then(() => setDirty(false))
              .catch(() => {});
          }}
        >
          保存更改
        </Button>
      </PageTitle>
      <section className="settings-section">
        <div className="settings-section-title">
          <LockKeyhole size={18} />
          <h2>X 登录 Cookie</h2>
          <Status value={data.cookie.status} />
        </div>
        <div className="cookie-settings">
          <div>
            <p className={data.cookie.status === "invalid" ? "error-text" : ""}>
              {data.cookie.reason}
            </p>
            <span className="muted">
              最近检测：
              {data.cookie.checked_at
                ? date(data.cookie.checked_at)
                : "尚未检测"}
            </span>
          </div>
          <form
            className="cookie-form"
            onSubmit={(e) => {
              e.preventDefault();
              if (!authToken.trim() || !ct0.trim() || saveCookie.isPending) return;
              void saveCookie
                .mutateAsync()
                .then(() => {
                  setAuthToken("");
                  setCt0("");
                })
                .catch(() => {});
            }}
          >
            <div className="cookie-fields">
              <div className="cookie-field">
                <label htmlFor="x-auth-token">auth_token</label>
                <Input
                  id="x-auth-token"
                  aria-label="auth_token"
                  aria-describedby="x-cookie-values-hint"
                  name="x-auth-token"
                  mode="password"
                  value={authToken}
                  onChange={setAuthToken}
                  placeholder="粘贴 auth_token 的 Value"
                  maxLength={1024 * 1024}
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  required
                  disabled={saveCookie.isPending}
                />
              </div>
              <div className="cookie-field">
                <label htmlFor="x-ct0">ct0</label>
                <Input
                  id="x-ct0"
                  aria-label="ct0"
                  aria-describedby="x-cookie-values-hint"
                  name="x-ct0"
                  mode="password"
                  value={ct0}
                  onChange={setCt0}
                  placeholder="粘贴 ct0 的 Value"
                  maxLength={1024 * 1024}
                  autoComplete="off"
                  autoCapitalize="off"
                  spellCheck={false}
                  required
                  disabled={saveCookie.isPending}
                />
              </div>
            </div>
            <p id="x-cookie-values-hint" className="cookie-values-hint">
              只粘贴各自的 Value（值），无需填写 <code>auth_token=</code> 或{" "}
              <code>ct0=</code>。已保存的值不会回显。
            </p>
            <div className="settings-actions">
              <Button
                htmlType="submit"
                theme="solid"
                icon={<Check size={15} />}
                loading={saveCookie.isPending}
                disabled={!authToken.trim() || !ct0.trim()}
              >
                保存并检测
              </Button>
              <Button
                icon={<RefreshCw size={15} />}
                disabled={data.cookie.status === "missing" || saveCookie.isPending}
                loading={check.isPending}
                onClick={() => check.mutate()}
              >
                检测
              </Button>
              <IconButton
                label="删除 Cookie"
                icon={<Trash2 size={16} />}
                disabled={data.cookie.status === "missing" || saveCookie.isPending}
                onClick={() =>
                  Modal.confirm({
                    title: "删除登录 Cookie？",
                    content: "需要认证的同步将停止，归档内容会保留。",
                    okText: "删除",
                    cancelText: "取消",
                    onOk: () => remove.mutateAsync(),
                  })
                }
              />
            </div>
          </form>
          <details
            className="cookie-guide"
            open={data.cookie.status === "missing"}
          >
            <summary>
              <Info size={15} />
              如何获取 Cookie
              <ChevronRight size={15} className="cookie-guide-chevron" />
            </summary>
            <ol>
              <li>
                在电脑浏览器打开{" "}
                <a href="https://x.com" target="_blank" rel="noreferrer">
                  X <ExternalLink size={12} />
                </a>
                ，登录自己的账号。
              </li>
              <li>
                右键页面选择“检查”，打开开发者工具。Chrome / Edge 进入{" "}
                <strong>Application（应用） &gt; Storage &gt; Cookies</strong>；Firefox
                进入 <strong>Storage（存储） &gt; Cookies</strong>。
              </li>
              <li>
                选择 <code>https://x.com</code>，在 Name（名称）列找到{" "}
                <code>auth_token</code> 和 <code>ct0</code>，分别复制对应的{" "}
                <strong>Value（值）</strong>。若未找到，刷新 X 页面后重试。
              </li>
              <li>
                将两个值填入上方对应输入框，点击“保存并检测”，等待状态变为“有效”。
                会话失效时，从同一登录会话重新复制这两个值并更新。
              </li>
            </ol>
            <p className="cookie-guide-note">
              Cookie 等同登录凭据，请勿分享给他人。
            </p>
          </details>
        </div>
      </section>
      <section className="settings-section">
        <div className="settings-section-title">
          <RefreshCw size={18} />
          <h2>同步与下载</h2>
        </div>
        <SettingRow label="默认同步间隔">
          <InputNumber
            aria-label="默认同步间隔"
            value={draft.sync_interval_minutes}
            min={5}
            max={43200}
            suffix="分钟"
            onChange={(v) => change("sync_interval_minutes", Number(v))}
          />
        </SettingRow>
        <SettingRow label="下载并发数">
          <InputNumber
            aria-label="下载并发数"
            value={draft.download_concurrency}
            min={1}
            max={8}
            onChange={(v) => change("download_concurrency", Number(v))}
          />
        </SettingRow>
        <SettingRow label="自动下载媒体">
          <Switch
            aria-label="自动下载媒体"
            checked={draft.auto_download}
            onChange={(v) => change("auto_download", v)}
          />
        </SettingRow>
        <SettingRow label="定时关注关系快照">
          <Switch
            aria-label="定时关注关系快照"
            checked={draft.relationship_sync_enabled}
            onChange={(v) => change("relationship_sync_enabled", v)}
          />
        </SettingRow>
        {draft.relationship_sync_enabled && (
          <SettingRow label="关系快照间隔">
            <InputNumber
              aria-label="关系快照间隔"
              value={draft.relationship_interval_minutes}
              min={60}
              max={43200}
              suffix="分钟"
              onChange={(v) =>
                change("relationship_interval_minutes", Number(v))
              }
            />
          </SettingRow>
        )}
      </section>
      <section className="settings-section">
        <div className="settings-section-title">
          <FolderOpen size={18} />
          <h2>存储</h2>
        </div>
        <SettingRow label="图片保存路径">
          <div className="path-input">
            <span>数据目录 /</span>
            <Input
              aria-label="图片保存路径"
              value={draft.image_path}
              onChange={(v) => change("image_path", v)}
            />
            <span>/ 用户名 /</span>
          </div>
        </SettingRow>
        <SettingRow label="视频保存路径">
          <div className="path-input">
            <span>数据目录 /</span>
            <Input
              aria-label="视频保存路径"
              value={draft.video_path}
              onChange={(v) => change("video_path", v)}
            />
            <span>/ 用户名 /</span>
          </div>
        </SettingRow>
        <div className="storage-settings">
          <div>
            <span>归档媒体</span>
            <strong>{bytes(data.storage.media_bytes)}</strong>
          </div>
          <div>
            <span>数据库</span>
            <strong>{bytes(data.storage.database_bytes)}</strong>
          </div>
          <div>
            <span>磁盘可用</span>
            <strong>{bytes(data.storage.free_bytes)}</strong>
          </div>
          <div>
            <span>磁盘总计</span>
            <strong>{bytes(data.storage.total_bytes)}</strong>
          </div>
        </div>
      </section>
      <section className="settings-section">
        <div className="settings-section-title">
          <SettingsIcon size={18} />
          <h2>运行环境</h2>
          <span className="muted">V{version}</span>
        </div>
        {Object.entries(data.tools).map(([tool, state]) => (
          <div className="tool-row" key={tool}>
            <span className={state.available ? "tool-ok" : "error-text"}>
              {state.available ? (
                <Check size={15} />
              ) : (
                <CircleAlert size={15} />
              )}
              {tool}
            </span>
            <code>{state.version || "未安装"}</code>
          </div>
        ))}
      </section>
    </>
  );
}
function SettingRow({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="setting-row">
      <span>{label}</span>
      <div>{children}</div>
    </div>
  );
}
