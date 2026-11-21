import type { CSSProperties } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { Activity, ChevronRight, LayoutDashboard, ListTodo, MessagesSquare, Minus, Monitor, Moon, Settings, Square, Sun, X } from "lucide-react";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuItem,
  SidebarMenuButton,
  SidebarProvider,
  SidebarRail,
  SidebarSeparator,
  SidebarTrigger,
} from "@/components/ui/sidebar";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { useTheme } from "@/lib/use-theme";
import { cn } from "@/lib/utils";
import { PersistentHarnessDetail } from "@/live/live-harness";
import { harnessEntryPath } from "@/live/last-harness-device";

const DESKTOP_QUERY = typeof window !== "undefined" ? new URLSearchParams(window.location.search) : null;
const IS_MAC_DESKTOP = Boolean(DESKTOP_QUERY?.has("desktop")) && DESKTOP_QUERY?.get("os") !== "windows";
const IS_WINDOWS_DESKTOP = DESKTOP_QUERY?.get("os") === "windows";

function MeerkatMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="none" className={className} aria-hidden="true">
      <circle cx="12" cy="12" r="8.6" stroke="currentColor" strokeWidth="1.6" strokeOpacity="0.55" />
      <circle cx="12" cy="12" r="4.6" stroke="currentColor" strokeWidth="1.8" />
      <circle cx="12" cy="12" r="1.4" fill="currentColor" />
      <circle cx="12" cy="3.4" r="1.3" fill="currentColor" />
      <circle cx="7.3" cy="18.6" r="1.3" fill="currentColor" />
      <circle cx="16.7" cy="18.6" r="1.3" fill="currentColor" />
    </svg>
  );
}

function WindowControls() {
  const action = (name: "minimise" | "toggle-maximise" | "close") => {
    void fetch("/desktop/window/" + name, { method: "POST" }).catch(() => undefined);
  };

  return (
    <div
      className="ml-1 flex h-full items-center"
      style={{ "--wails-draggable": "no-drag" } as CSSProperties}
    >
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-9 rounded-none text-muted-foreground hover:bg-accent"
        aria-label="最小化"
        data-testid="window-minimise"
        onClick={() => action("minimise")}
      >
        <Minus className="size-3.5" />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-9 rounded-none text-muted-foreground hover:bg-accent"
        aria-label="最大化或还原"
        data-testid="window-maximise"
        onClick={() => action("toggle-maximise")}
      >
        <Square className="size-3" />
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="size-9 rounded-none text-muted-foreground hover:bg-rose-500 hover:text-white"
        aria-label="关闭"
        data-testid="window-close"
        onClick={() => action("close")}
      >
        <X className="size-4" />
      </Button>
    </div>
  );
}

const liveNavItems = [
  { to: "/live/harness", label: "DSH", icon: Monitor, end: false },
  { to: "/live", label: "实时设备", icon: Activity, end: true },
  { to: "/live/board", label: "任务看板", icon: LayoutDashboard, end: false },
  { to: "/live/tasks", label: "实时任务", icon: ListTodo, end: false },
  { to: "/live/sessions", label: "实时会话", icon: MessagesSquare, end: false },
];

type Crumb = { label: string; to?: string };

function useBreadcrumb(): Crumb[] {
  const { pathname } = useLocation();
  const segments = pathname.split("/").filter(Boolean);
  if (segments[0] !== "live") return [{ label: "实时设备", to: "/live" }];
  if (segments[1] === "tasks") {
    const crumbs: Crumb[] = [{ label: "实时任务", to: "/live/tasks" }];
    if (segments[2]) crumbs.push({ label: "任务详情" });
    return crumbs;
  }
  if (segments[1] === "harness") {
    const crumbs: Crumb[] = [{ label: "DSH", to: harnessEntryPath() }];
    if (segments[2]) crumbs.push({ label: "设备" });
    return crumbs;
  }
  if (segments[1] === "settings") return [{ label: "设置" }];
  if (segments[1] === "board") return [{ label: "任务看板", to: "/live/board" }];
  if (segments[1] === "task-pool") return [{ label: "任务看板", to: "/live/board" }, { label: segments[2] === "new" ? "新任务" : "任务详情" }];
  if (segments[1] === "sessions") return [{ label: "实时会话", to: "/live/sessions" }];
  const crumbs: Crumb[] = [{ label: "实时设备", to: "/live" }];
  if (segments[1] === "devices") crumbs.push({ label: "设备详情" });
  return crumbs;
}

function Topbar() {
  const { theme, toggle } = useTheme();
  const crumbs = useBreadcrumb();

  return (
    <header className={cn("sticky top-0 z-20 flex h-14 shrink-0 items-center gap-3 border-b bg-background/85 px-4 backdrop-blur lg:px-6", IS_MAC_DESKTOP && "h-[calc(3.5rem+2rem)] pt-8")}>
      <SidebarTrigger className="-ml-1" />
      <Separator orientation="vertical" className="mr-1 h-4" />
      <nav aria-label="面包屑" className="flex min-w-0 items-center gap-1.5 text-sm">
        <span className="hidden text-muted-foreground sm:inline">Meerkat</span>
        {crumbs.map((crumb, index) => (
          <span key={index} className="flex min-w-0 items-center gap-1.5">
            <ChevronRight className="hidden size-3.5 shrink-0 text-muted-foreground/50 sm:inline" />
            {crumb.to ? (
              <Link to={crumb.to} className="truncate text-muted-foreground transition-colors hover:text-foreground">
                {crumb.label}
              </Link>
            ) : (
              <span className="truncate font-medium">{crumb.label}</span>
            )}
          </span>
        ))}
      </nav>
      <div
        className="h-full min-w-4 flex-1"
        style={{ "--wails-draggable": "drag" } as CSSProperties}
        onDoubleClick={() => {
          if (!IS_WINDOWS_DESKTOP) return;
          void fetch("/desktop/window/toggle-maximise", { method: "POST" }).catch(() => undefined);
        }}
      />
      <div className="ml-auto flex items-center gap-1.5">
        <span className="hidden items-center gap-1.5 text-xs text-muted-foreground md:flex">
          <span className="relative flex size-1.5">
            <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-500 opacity-60" />
            <span className="relative inline-flex size-1.5 rounded-full bg-emerald-500" />
          </span>
          实时数据
        </span>
        <Button
          variant="ghost"
          size="icon"
          className="size-8 text-muted-foreground"
          onClick={toggle}
          aria-label={theme === "dark" ? "切换到浅色模式" : "切换到深色模式"}
        >
          {theme === "dark" ? <Sun className="size-4" /> : <Moon className="size-4" />}
        </Button>
        <Separator orientation="vertical" className="mx-1 h-4" />
        <Avatar className="size-7">
          <AvatarFallback className="bg-secondary text-[11px] font-semibold">B</AvatarFallback>
        </Avatar>
        {IS_WINDOWS_DESKTOP ? <WindowControls /> : null}
      </div>
    </header>
  );
}

export function AppShell() {
  const location = useLocation();
  const harnessDetailActive = /^\/live\/harness\/[^/]+$/.test(location.pathname);
  const harnessSectionActive = location.pathname.startsWith("/live/harness");
  return (
    <SidebarProvider className={IS_MAC_DESKTOP ? "h-svh min-h-0 overflow-hidden" : undefined}>
      <Sidebar collapsible="icon">
        <SidebarHeader className={IS_MAC_DESKTOP ? "pt-10" : undefined}>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" asChild>
                <Link to="/live">
                  <div className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary text-primary-foreground">
                    <MeerkatMark className="size-5" />
                  </div>
                  <div className="grid flex-1 text-left leading-tight">
                    <span className="truncate text-sm font-semibold">Meerkat</span>
                    <span className="truncate text-[11px] text-muted-foreground">狐獴 · 多设备总控</span>
                  </div>
                </Link>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarHeader>
        <SidebarSeparator />
        <SidebarContent>
          <SidebarGroup>
            <SidebarGroupLabel>实时</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {liveNavItems.map((item) => {
                  // DSH 入口跟随上次打开的设备；列表页仍由详情页的「切换设备」进入。
                  const target = item.to === "/live/harness" ? harnessEntryPath() : item.to;
                  const poolSectionActive = item.to === "/live/board" && location.pathname.startsWith("/live/task-pool/");
                  const sectionActive = (item.to === "/live/harness" && harnessSectionActive) || poolSectionActive;
                  return (
                    <SidebarMenuItem key={item.to}>
                      <SidebarMenuButton asChild tooltip={item.label}>
                        <NavLink
                          to={target}
                          end={item.end}
                          aria-current={poolSectionActive ? "location" : undefined}
                          className={({ isActive }) =>
                            cn(
                              "group-data-[active=true]:bg-sidebar-accent",
                              (isActive || sectionActive) && "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                            )
                          }
                        >
                          <item.icon />
                          <span>{item.label}</span>
                        </NavLink>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  );
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
          <SidebarGroup>
            <SidebarGroupLabel>系统</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild tooltip="设置">
                    <NavLink
                      to="/live/settings"
                      className={({ isActive }) =>
                        cn("group-data-[active=true]:bg-sidebar-accent", isActive && "bg-sidebar-accent font-medium text-sidebar-accent-foreground")
                      }
                    >
                      <Settings />
                      <span>设置</span>
                    </NavLink>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        </SidebarContent>
        <SidebarFooter>
          <div className="rounded-lg border bg-card p-2.5 text-[11px] leading-relaxed text-muted-foreground group-data-[collapsible=icon]:hidden">
            <div className="flex items-center gap-1.5 font-medium text-foreground">
              <span className="relative flex size-1.5">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-500 opacity-60" />
                <span className="relative inline-flex size-1.5 rounded-full bg-emerald-500" />
              </span>
              实时数据
            </div>
            <div className="mt-0.5">中心 + Go Edge · 协议 v1</div>
            <div className="mt-1.5">设备 · 任务 · 会话实时同步</div>
          </div>
          <SidebarMenu>
            <SidebarMenuItem>
              <SidebarMenuButton size="lg" asChild>
                <div className="cursor-default">
                  <Avatar className="size-7">
                    <AvatarFallback className="bg-secondary text-[11px] font-semibold">M</AvatarFallback>
                  </Avatar>
                  <div className="grid flex-1 text-left leading-tight">
                    <span className="truncate text-xs font-medium">本机用户</span>
                    <span className="truncate text-[10px] text-muted-foreground">单用户模式</span>
                  </div>
                </div>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarFooter>
        <SidebarRail />
      </Sidebar>
      <SidebarInset className={cn("min-w-0", IS_MAC_DESKTOP && "h-svh min-h-0 overflow-hidden")}>
        <Topbar />
        <main className={cn("flex min-w-0 flex-1 flex-col gap-5 p-4 lg:p-6", IS_MAC_DESKTOP && "min-h-0 overflow-y-auto")}>
          <div className={cn("mx-auto w-full min-w-0 max-w-[1440px] flex-1", harnessDetailActive && "hidden")}>
            <Outlet />
          </div>
          <div className={cn("mx-auto w-full min-w-0 max-w-[1440px] min-h-0 flex-1", !harnessDetailActive && "hidden")}>
            <PersistentHarnessDetail />
          </div>
        </main>
      </SidebarInset>
    </SidebarProvider>
  );
}