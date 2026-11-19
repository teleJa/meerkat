import { useQuery } from "@tanstack/react-query";
import { Download, FileText, Image as ImageIcon, ExternalLink } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { EmptyState } from "@/live/live-shared";
import { artifactContentURL, formatBytes, formatDateTime, type LiveArtifact } from "@/live/api";

function isPreviewableText(artifact: LiveArtifact) {
  return artifact.mime.startsWith("text/") || artifact.mime === "application/json" ||
    artifact.mime === "image/svg+xml" || artifact.mime === "application/javascript";
}

function isImage(artifact: LiveArtifact) {
  return artifact.mime.startsWith("image/") && artifact.mime !== "image/svg+xml";
}

function TextArtifact({ artifact }: { artifact: LiveArtifact }) {
  const query = useQuery({
    queryKey: ["live", "artifact", artifact.id, "text"],
    queryFn: async () => {
      const response = await fetch(artifactContentURL(artifact.id));
      if (!response.ok) throw new Error("读取文件失败");
      return response.text();
    },
    retry: false,
  });
  if (query.isLoading) return <div className="h-32 animate-pulse rounded-lg bg-muted" />;
  if (query.isError) return <p className="text-xs text-rose-500">无法读取文件内容</p>;
  return (
    <pre className="max-h-[420px] overflow-auto whitespace-pre-wrap break-words rounded-lg border bg-muted/30 p-3 font-mono text-[11px] leading-relaxed">
      {String(query.data ?? "").slice(0, 200_000)}
    </pre>
  );
}

function ArtifactPreview({ artifact }: { artifact: LiveArtifact }) {
  const url = artifactContentURL(artifact.id);
  if (isImage(artifact)) {
    return (
      <a href={url} target="_blank" rel="noreferrer" className="block">
        <img src={url} alt={artifact.name} className="max-h-[420px] w-full rounded-lg border object-contain" />
      </a>
    );
  }
  if (artifact.mime.startsWith("text/html") || artifact.mime.startsWith("application/pdf")) {
    return (
      <iframe
        title={artifact.name}
        src={url}
        sandbox={artifact.mime.startsWith("text/html") ? "allow-scripts" : undefined}
        className="h-[460px] w-full rounded-lg border bg-white"
      />
    );
  }
  if (isPreviewableText(artifact)) {
    return <TextArtifact artifact={artifact} />;
  }
  return (
    <div className="flex items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-xs text-muted-foreground">
      <FileText className="size-4" />
      该类型暂不内嵌预览，可下载后查看。
    </div>
  );
}

export function LiveArtifactPanel({ artifacts }: { artifacts: LiveArtifact[] }) {
  if (artifacts.length === 0) {
    return (
      <EmptyState
        title="暂无工件"
        hint="Agent 在允许目录内生成或修改的文件，会在轮次结束后自动出现在这里。"
      />
    );
  }
  return (
    <div className="space-y-4">
      {artifacts.map((artifact) => (
        <Card key={artifact.id} className="gap-0 py-0">
          <CardHeader className="flex flex-row items-start justify-between gap-3 px-4 py-3">
            <div className="min-w-0">
              <CardTitle className="flex items-center gap-2 truncate text-sm">
                {isImage(artifact) ? <ImageIcon className="size-4 shrink-0" /> : <FileText className="size-4 shrink-0" />}
                <span className="truncate">{artifact.name}</span>
              </CardTitle>
              <p className="mt-1 truncate font-mono text-[10px] text-muted-foreground">{artifact.path}</p>
              <p className="mt-1 text-[10px] text-muted-foreground">
                {formatBytes(artifact.size)} · {artifact.mime} · {formatDateTime(artifact.created_at)}
              </p>
            </div>
            <div className="flex shrink-0 gap-1">
              <Button asChild variant="outline" size="sm">
                <a href={artifactContentURL(artifact.id)} target="_blank" rel="noreferrer">
                  <ExternalLink className="size-3.5" />
                  打开
                </a>
              </Button>
              <Button asChild variant="outline" size="sm">
                <a href={artifactContentURL(artifact.id)} download={artifact.name}>
                  <Download className="size-3.5" />
                  下载
                </a>
              </Button>
            </div>
          </CardHeader>
          <CardContent className="px-4 pb-4">
            <ArtifactPreview artifact={artifact} />
          </CardContent>
        </Card>
      ))}
    </div>
  );
}