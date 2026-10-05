# Hooks de inicializacao do LocalStack.
#
# Os arquivos desta pasta sao executados uma vez, no start do LocalStack, e e o
# lugar onde as filas e a politica de redrive nascem por codigo.
#
# A ideia e deliberada: uma fila criada a mao no console e uma fila que o outro
# ambiente nao tem. Provisionar por script faz o teste de integracao depender
# apenas de `docker compose up -d`.
#
# Nada aqui ainda: as filas `wager-transactions.fifo` e
# `wager-transactions-dlq.fifo` entram na etapa de consumidor SQS, junto com a
# decisao de MessageGroupId, MessageDeduplicationId, visibility timeout e limite
# de tentativas. Criar a fila antes de decidir esses parametros seria escolher a
# politica duas vezes, e a segunda em silencio.
