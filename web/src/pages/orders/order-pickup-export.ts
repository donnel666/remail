type PickupURLExportEntry = { email: string; url: string };

export function formatPickupURLExport(entries: PickupURLExportEntry[]) {
  return entries.map(({ email, url }) => `${email}----${url}`).join("\n");
}

export function exportPickupURLs(entries: PickupURLExportEntry[]) {
  const objectURL = URL.createObjectURL(
    new Blob([formatPickupURLExport(entries)], {
      type: "text/plain;charset=utf-8",
    })
  );
  const link = document.createElement("a");
  link.href = objectURL;
  link.download = "pickup-urls.txt";
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(objectURL);
}
