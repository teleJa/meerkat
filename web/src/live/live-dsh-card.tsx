import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Loader2, Plus } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { errorMessage, liveApi, type DshState, type LiveDevice } from "@/live/api";

const statusText = (dsh: DshState | undefined): { label: string; className: string } => {
  if (!dsh) return { label: "状态未知", className: "text-muted-foreground" };
  if (!dsh.installed && dsh.profiles.length === 0) return { label: "未安装", className: "text-amber-600" };
  switch (dsh.status) {
    case "running":
      return {
        label: dsh.owner === "external" ? "运行中 · 外部实例" : "运行中 · Meerkat 启动",
        className: "text-emerald-600",
      };
    case "starting":
      return { label: "正在启动…", className: "text-sky-600" };
    case "error":
      return { label: "启动失败", className: "text-rose-600" };
    default:
      return { label: "已停止", className: "text-muted-foreground" };
  }
};

// 一台设备的 DSH 卡片：profile 选择、状态与唯一主操作。
export function DshDeviceCard({
  device,
  localDeviceId,
}: {
  device: LiveDevice;
  localDeviceId: string;
}) {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [newName, setNewName] = useState("");

  const statusQuery = useQuery({
    queryKey: ["live", "dsh", device.id],
    queryFn: () => liveApi.dshStatus(device.id),
    refetchInterval: 3000,
    retry: false,
  });
  const dsh = statusQuery.data?.dsh ?? device.dsh ?? undefined;
  const online = device.status === "online";
  const running = dsh?.status === "running" && Boolean(dsh.has_url);
  const selectedProfile = dsh?.profile ?? "";
  const selected = dsh?.profiles.find((profile) => profile.name === selectedProfile);
  const status = statusText(dsh);

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["live", "dsh", device.id] }),
      queryClient.invalidateQueries({ queryKey: ["live", "devices"] }),
    ]);
  };

  const profileMutation = useMutation({
    mutationFn: (profile: string) => liveApi.setDshProfile(device.id, profile),
    onSuccess: () => {
      setNotice("");
      void refresh();
    },
    onError: (error) => setNotice("保存 profile 失败：" + errorMessage(error)),
  });

  const actionMutation = useMutation({
    mutationFn: (action: "connect" | "launch" | "stop") =>
      liveApi.dshAction(device.id, action, selectedProfile || undefined),
    onSuccess: () => {
      setNotice("");
      void refresh();
    },
    onError: (error) => setNotice("操作失败：" + errorMessage(error)),
  });

  const createMutation = useMutation({
    mutationFn: async (name: string) => {
      await liveApi.createDshProfile(device.id, name);
      // 创建结果要等 Edge 执行完；选择先保存，用户随后点启动/连接。
      await liveApi.setDshProfile(device.id, name);
    },
    onSuccess: () => {
      setCreateOpen(false);
      setNewName("");
      setNotice("已创建 profile，点「启动」拉起实例。");
      void refresh();
    },
    onError: (error) => setNotice("新建 profile 失败：" + errorMessage(error)),
  });

  const installMutation = useMutation({
    mutationFn: () => liveApi.installDsh(device.id),
    onSuccess: (result) => {
      setNotice(result.already ? "DSH 已经可用" : "已下发安装指令，完成后会自动启动 DSH。");
      void refresh();
    },
    onError: (error) => setNotice("安装失败：" + errorMessage(error)),
  });

  const primaryAction = (() => {
    if (!online) {
      return <Button size="sm" className="h-7 px-2 text-[11px]" disabled>设备离线</Button>;
    }
    if (installMutation.isPending) {
      return (
        <Button size="sm" className="h-7 gap-1 px-2 text-[11px]" disabled>
          <Loader2 className="size-3 animate-spin" />安装中…
        </Button>
      );
    }
    if (dsh && !dsh.installed && dsh.profiles.length === 0) {
      return (
        <Button size="sm" className="h-7 px-2 text-[11px]" onClick={() => installMutation.mutate()}>
          安装 DSH
        </Button>
      );
    }
    if (dsh?.status === "starting") {
      return (
        <Button size="sm" className="h-7 gap-1 px-2 text-[11px]" disabled>
          <Loader2 className="size-3 animate-spin" />正在启动…
        </Button>
      );
    }
    if (running) {
      return (
        <Button size="sm" className="h-7 px-2 text-[11px]" asChild>
          <Link to={"/live/harness/" + device.id}>打开 DSH</Link>
        </Button>
      );
    }
    if (dsh?.status === "error") {
      const retry = dsh.mode === "connect" ? "connect" : "launch";
      return (
        <Button
          size="sm"
          className="h-7 px-2 text-[11px]"
          disabled={actionMutation.isPending || !selectedProfile}
          onClick={() => actionMutation.mutate(retry)}
        >
          重试{retry === "connect" ? "连接" : "启动"}
        </Button>
      );
    }
    if (selected?.running) {
      return (
        <Button
          size="sm"
          className="h-7 px-2 text-[11px]"
          disabled={actionMutation.isPending}
          onClick={() => actionMutation.mutate("connect")}
        >
          连接
        </Button>
      );
    }
    return (
      <Button
        size="sm"
        className="h-7 px-2 text-[11px]"
        disabled={actionMutation.isPending || !selectedProfile || !dsh}
        onClick={() => actionMutation.mutate("launch")}
      >
        启动
      </Button>
    );
  })();

  // Hub 未上报 dsh（旧 Edge）时保持原来的行为。
  if (!dsh) {
    return (
      <div className={device.harness_available ? "rounded-xl border bg-card p-4" : "rounded-xl border border-dashed bg-muted/20 p-4"}>
        <div className="flex items-center justify-between gap-2">
          <span className="truncate text-sm font-medium">{device.name}</span>
          {device.id === localDeviceId ? (
            <span className="rounded-full border border-sky-200 bg-sky-50 px-1.5 py-0.5 text-[10px] font-medium text-sky-700">本机</span>
          ) : null}
        </div>
        <div className="mt-1 truncate text-[11px] text-muted-foreground">{device.hostname} · {device.os}</div>
        <div className="mt-3">
          {device.harness_available ? (
            <Button size="sm" className="h-7 px-2 text-[11px]" asChild>
              <Link to={"/live/harness/" + device.id}>打开 DSH</Link>
            </Button>
          ) : (
            <Button
              size="sm"
              className="h-7 px-2 text-[11px]"
              disabled={!online || installMutation.isPending}
              onClick={() => installMutation.mutate()}
            >
              {online ? "安装 DSH" : "设备离线"}
            </Button>
          )}
        </div>
        {notice ? <div className="mt-2 text-[11px] text-muted-foreground">{notice}</div> : null}
      </div>
    );
  }

  return (
    <div className="rounded-xl border bg-card p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-sm font-medium">{device.name}</span>
        <div className="flex items-center gap-1.5">
          {device.id === localDeviceId ? (
            <span className="rounded-full border border-sky-200 bg-sky-50 px-1.5 py-0.5 text-[10px] font-medium text-sky-700">本机</span>
          ) : null}
          <span className={device.status === "online" ? "text-[10px] text-emerald-600" : "text-[10px] text-muted-foreground"}>
            {device.status === "online" ? "在线" : device.status === "stale" ? "状态陈旧" : "离线"}
          </span>
        </div>
      </div>
      <div className="mt-1 truncate text-[11px] text-muted-foreground">{device.hostname} · {device.os}</div>

      <div className="mt-3 flex items-center gap-2">
        <Select
          value={selectedProfile}
          onValueChange={(value) => profileMutation.mutate(value)}
          disabled={!online || profileMutation.isPending || dsh.profiles.length === 0}
        >
          <SelectTrigger className="h-8 min-w-0 flex-1 text-xs" data-testid={"dsh-profile-" + device.id}>
            <SelectValue placeholder={dsh.profiles.length === 0 ? "没有可用 profile" : "选择 profile"} />
          </SelectTrigger>
          <SelectContent>
            {dsh.profiles.map((profile) => (
              <SelectItem key={profile.name} value={profile.name}>
                {profile.name}
                {profile.running ? " · 运行中" : profile.available ? "" : " · 不可用"}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button variant="ghost" size="sm" className="h-8 gap-1 px-2 text-[11px]" disabled={!online}>
              <Plus className="size-3" />新建
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-sm">
            <DialogHeader>
              <DialogTitle>新建 DSH profile</DialogTitle>
              <DialogDescription>
                在 {device.name} 上按 web 模板创建一个 profile，不会启动服务；创建后可直接启动。
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-2">
              <Input
                value={newName}
                onChange={(event) => setNewName(event.target.value)}
                placeholder="例如 probe"
                autoFocus
              />
              <p className="text-[11px] text-muted-foreground">小写字母、数字、点、下划线或连字符，最多 64 个字符。</p>
            </div>
            {createMutation.isError ? (
              <p className="text-[11px] text-rose-600">{errorMessage(createMutation.error)}</p>
            ) : null}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setCreateOpen(false)}>取消</Button>
              <Button
                size="sm"
                disabled={createMutation.isPending || newName.trim() === ""}
                onClick={() => createMutation.mutate(newName.trim())}
              >
                {createMutation.isPending ? "创建中…" : "创建"}
              </Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>

      <div className="mt-2 flex items-center justify-between gap-2">
        <span className={"text-[11px] " + status.className}>{status.label}</span>
        <div className="flex items-center gap-1">
          {running && dsh.owner === "external" ? (
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-[11px]"
              disabled={actionMutation.isPending}
              onClick={() => actionMutation.mutate("stop")}
            >
              断开
            </Button>
          ) : null}
          {running && dsh.owner !== "external" ? (
            <Button
              variant="outline"
              size="sm"
              className="h-7 px-2 text-[11px]"
              disabled={actionMutation.isPending}
              onClick={() => actionMutation.mutate("stop")}
            >
              停止
            </Button>
          ) : null}
          {primaryAction}
        </div>
      </div>
      {running && selectedProfile !== "" && dsh.profile !== "" && selectedProfile !== dsh.profile ? (
        <p className="mt-1 text-[11px] text-muted-foreground">
          当前运行的是 {dsh.profile}；切换到 {selectedProfile} 请点「启动」或「连接」。
        </p>
      ) : null}
      {dsh.error ? (
        // 「未找到 dsh」这类错误本身就是操作指引，需要换行完整显示，不能截断。
        <p
          className={
            dsh.error.includes("未找到 dsh")
              ? "mt-1 whitespace-pre-line break-words text-[11px] leading-relaxed text-rose-600"
              : "mt-1 truncate text-[11px] text-rose-600"
          }
          title={dsh.error}
        >
          {dsh.error}
        </p>
      ) : null}
      {notice ? <p className="mt-1 text-[11px] text-muted-foreground">{notice}</p> : null}
      {statusQuery.isError ? (
        <p className="mt-1 text-[11px] text-muted-foreground">状态读取失败：{errorMessage(statusQuery.error)}</p>
      ) : null}
    </div>
  );
}
