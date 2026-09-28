/** The DATBase wordmark: bold, "DAT" in brand blue, "Base" in brand green, with the square mark. */
export function Logo({ size = "md" }: { size?: "md" | "lg" }) {
  const box = size === "lg" ? "h-9 w-9 text-lg" : "h-7 w-7 text-sm";
  const text = size === "lg" ? "text-3xl" : "text-lg";
  return (
    <span className="inline-flex items-center gap-2" aria-label="DATBase">
      <span
        aria-hidden
        className={`inline-flex ${box} items-center justify-center rounded-md font-bold text-white`}
        style={{ background: "linear-gradient(135deg, #2d7daf 0%, #2d7daf 45%, #61b895 100%)" }}
      >
        D
      </span>
      <span className={`${text} font-bold tracking-tight`}>
        <span style={{ color: "#2d7daf" }}>DAT</span>
        <span style={{ color: "#61b895" }}>Base</span>
      </span>
    </span>
  );
}
