#import "/shared/common.typ": load-input, total-minor, money
#set text(font: "DejaVu Sans", lang: "ru")
#let data = load-input()
= Договор
Заказчик: #data.customer.name
Стоимость услуг: #money(total-minor(data.items)).
#if data.at("show_terms", default: true) [
  Условия выполнения определяются согласованным составом услуг.
]
