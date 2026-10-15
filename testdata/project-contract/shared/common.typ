#let load-input() = json(sys.inputs.at("leafrun-data", default: "/data/example.json"))
#let total-minor(items) = items.map(item => item.quantity * item.price_minor).sum(default: 0)
#let money(value) = {
  let major = calc.quo(value, 100)
  let minor = str(calc.rem(value, 100))
  str(major) + "." + if minor.len() == 1 { "0" + minor } else { minor }
}
#let item-table(items) = table(
  columns: (1fr, auto, auto, auto),
  table.header([Позиция], [Кол-во], [Цена], [Сумма]),
  ..items.map(item => (
    [#item.name], [#item.quantity],
    [#money(item.price_minor)], [#money(item.quantity * item.price_minor)],
  )).flatten(),
)
