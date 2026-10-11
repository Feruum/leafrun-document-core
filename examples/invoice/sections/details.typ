#let invoice = json("/data.json")
#table(
  columns: (1fr, auto, auto),
  inset: 8pt,
  [Description], [Quantity], [Total],
  ..invoice.items.map(item => (item.description, str(item.quantity), str(item.total) + " " + invoice.currency)).flatten(),
)
Total: #invoice.total #invoice.currency
