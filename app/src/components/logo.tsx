/** The DATBase wordmark: bold, "DAT" in brand blue, "Base" in brand green. */
export function Logo({ size = "md" }: { size?: "md" | "lg" }) {
  const text = size === "lg" ? "text-3xl" : "text-lg";
  return (
    <span className="inline-flex items-center" aria-label="DATBase">
      <span className={`${text} font-bold tracking-tight`}>
        <span style={{ color: "#2d7daf" }}>DAT</span>
        <span style={{ color: "#61b895" }}>Base</span>
      </span>
    </span>
  );
}
