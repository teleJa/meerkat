import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Navigate, Route, Routes } from "react-router-dom";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AppShell } from "@/components/app-shell";
import { LiveBoardPage } from "@/live/live-board";
import { LiveHarnessPage } from "@/live/live-harness";
import { LiveSettingsPage } from "@/live/live-settings";
import { LiveDevicesPage } from "@/live/live-devices";
import { LiveDeviceDetailPage } from "@/live/live-device-detail";
import { LiveTasksPage } from "@/live/live-tasks";
import { LiveTaskDetailPage } from "@/live/live-task-detail";
import { LiveSessionsPage } from "@/live/live-sessions";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
  },
});

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <Routes>
          <Route element={<AppShell />}>
            <Route index element={<Navigate to="/live/harness" replace />} />
            <Route path="live" element={<LiveDevicesPage />} />
            <Route path="live/board" element={<LiveBoardPage />} />
            <Route path="live/devices/:deviceId" element={<LiveDeviceDetailPage />} />
            <Route path="live/tasks" element={<LiveTasksPage />} />
          <Route path="live/harness" element={<LiveHarnessPage />} />
          <Route path="live/harness/:deviceId" element={<></>} />
          <Route path="live/settings" element={<LiveSettingsPage />} />
            <Route path="live/tasks/:taskId" element={<LiveTaskDetailPage />} />
            <Route path="live/sessions" element={<LiveSessionsPage />} />
            <Route path="*" element={<Navigate to="/live/harness" replace />} />
          </Route>
        </Routes>
      </TooltipProvider>
    </QueryClientProvider>
  );
}