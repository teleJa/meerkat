import { useEffect, useRef, useState } from "react";
import * as echarts from "echarts";
import type { MetricPoint } from "@/data/types";

const colors = {
  cpu: "#0ea5e9",
  memory: "#8b5cf6",
  gpu: "#f59e0b",
};

export function MetricChart({
  data,
  showGpu,
  height = 230,
}: {
  data: MetricPoint[];
  showGpu: boolean;
  height?: number;
}) {
  const el = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);
  const [dark, setDark] = useState(false);

  useEffect(() => {
    setDark(document.documentElement.classList.contains("dark"));
    const observer = new MutationObserver(() => {
      setDark(document.documentElement.classList.contains("dark"));
    });
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (!el.current) return;
    chart.current = echarts.init(el.current);
    const observer = new ResizeObserver(() => chart.current?.resize());
    observer.observe(el.current);
    return () => {
      observer.disconnect();
      chart.current?.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    if (!chart.current) return;
    const axisColor = dark ? "#71717a" : "#a1a1aa";
    const splitColor = dark ? "rgba(255,255,255,0.06)" : "rgba(0,0,0,0.06)";
    const tooltipBg = dark ? "#18181b" : "#ffffff";
    const tooltipBorder = dark ? "#3f3f46" : "#e4e4e7";
    const tooltipText = dark ? "#fafafa" : "#18181b";

    const series = [
      {
        name: "CPU",
        key: "cpu" as const,
        color: colors.cpu,
      },
      {
        name: "内存",
        key: "memory" as const,
        color: colors.memory,
      },
      ...(showGpu ? [{ name: "GPU 利用率", key: "gpuUtil" as const, color: colors.gpu }] : []),
    ];

    chart.current.setOption({
      animationDuration: 400,
      grid: { left: 34, right: 10, top: 14, bottom: 26 },
      tooltip: {
        trigger: "axis",
        backgroundColor: tooltipBg,
        borderColor: tooltipBorder,
        borderWidth: 1,
        padding: [8, 10],
        textStyle: { color: tooltipText, fontSize: 11 },
        axisPointer: { lineStyle: { color: axisColor, type: "dashed" } },
        valueFormatter: (value: unknown) => (value == null ? "—" : String(value) + "%"),
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: data.map((point) => point.t),
        axisLine: { lineStyle: { color: splitColor } },
        axisTick: { show: false },
        axisLabel: { color: axisColor, fontSize: 10, interval: 3, margin: 10 },
      },
      yAxis: {
        type: "value",
        min: 0,
        max: 100,
        splitNumber: 4,
        splitLine: { lineStyle: { color: splitColor, type: "dashed" } },
        axisLabel: { color: axisColor, fontSize: 10, formatter: "{value}%" },
      },
      series: series.map((item) => ({
        name: item.name,
        type: "line",
        smooth: 0.35,
        showSymbol: false,
        symbolSize: 4,
        lineStyle: { width: 2, color: item.color },
        itemStyle: { color: item.color },
        areaStyle: {
          color: new echarts.graphic.LinearGradient(0, 0, 0, 1, [
            { offset: 0, color: item.color + "33" },
            { offset: 1, color: item.color + "00" },
          ]),
        },
        emphasis: { focus: "series" },
        data: data.map((point) => point[item.key] ?? null),
      })),
    });
  }, [data, showGpu, dark]);

  return <div ref={el} style={{ height }} className="w-full" />;
}

export function ChartLegend({ showGpu }: { showGpu: boolean }) {
  const items = [
    { label: "CPU", color: colors.cpu },
    { label: "内存", color: colors.memory },
    ...(showGpu ? [{ label: "GPU 利用率", color: colors.gpu }] : []),
  ];
  return (
    <div className="flex flex-wrap items-center gap-4">
      {items.map((item) => (
        <span key={item.label} className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <span className="size-2 rounded-full" style={{ backgroundColor: item.color }} />
          {item.label}
        </span>
      ))}
    </div>
  );
}
