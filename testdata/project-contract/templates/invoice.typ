#import "/shared/common.typ": load-input, total-minor, money, item-table
#set text(font: "DejaVu Sans", lang: "ru")
#let data = load-input()
= Счёт
Клиент: #data.customer.name
#item-table(data.items)
*Итого: #money(total-minor(data.items))*
#if data.at("show_terms", default: true) [
  Оплатите счёт в течение пяти рабочих дней.
]
