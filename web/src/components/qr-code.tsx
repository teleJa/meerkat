import { useMemo } from "react";
import * as qrcodeModule from "qrcode-generator";

// qrcode-generator 以 CommonJS `export =` 发布。这里在运行时取默认导出，
// 类型用本文件的窄接口描述，避免依赖包内声明在 bundler 解析下的互操作差异。
type QrCodeInstance = {
  addData: (data: string, mode?: "Byte") => void;
  make: () => void;
  getModuleCount: () => number;
  isDark: (row: number, col: number) => boolean;
};
type QrCodeFactory = (typeNumber: number, level: "L" | "M" | "Q" | "H") => QrCodeInstance;

const qrcodeFactory: QrCodeFactory =
  (qrcodeModule as unknown as { default?: QrCodeFactory }).default ??
  (qrcodeModule as unknown as QrCodeFactory);

// 静默区：标准要求至少 4 模块，这里用 2 保持视觉紧凑，常见扫码器仍可识别。
const QUIET_MODULES = 2;

/**
 * 把文本渲染成可扫描的二维码（纯 SVG，无图片请求）。
 * 只用于本机展示的邀请码；内容不落盘、不经过网络。
 */
export function QrCode({
  value,
  size = 168,
  label = "二维码",
  className,
}: {
  value: string;
  size?: number;
  label?: string;
  className?: string;
}) {
  const qr = useMemo(() => {
    if (!value) return null;
    const instance = qrcodeFactory(0, "M");
    instance.addData(value, "Byte");
    instance.make();
    const count = instance.getModuleCount();
    let path = "";
    for (let row = 0; row < count; row += 1) {
      for (let col = 0; col < count; col += 1) {
        if (instance.isDark(row, col)) {
          path += `M${col + QUIET_MODULES} ${row + QUIET_MODULES}h1v1h-1z`;
        }
      }
    }
    return { path, modules: count + QUIET_MODULES * 2 };
  }, [value]);

  if (!qr) return null;
  return (
    <svg
      className={className}
      width={size}
      height={size}
      viewBox={`0 0 ${qr.modules} ${qr.modules}`}
      role="img"
      aria-label={label}
      shapeRendering="crispEdges"
    >
      <rect width={qr.modules} height={qr.modules} fill="#ffffff" />
      <path d={qr.path} fill="#000000" />
    </svg>
  );
}
