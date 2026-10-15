#import "/shared/common.typ": load-input, total-minor, money, item-table
#set text(font: "DejaVu Sans", lang: "ru")
#let data = load-input()
= Акт
Заказчик: #data.customer.name
#item-table(data.items)
Услуги выполнены. Итого: #money(total-minor(data.items)).
